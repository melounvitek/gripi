package server

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/melounvitek/gripi/internal/agent"
)

// PCLinkPoll is how long a poll of the desktop app's link waits for messages. Tests shorten it.
var PCLinkPoll = 25 * time.Second

// PCLinkStale is how long after its last poll a link is lost, and until then keeps other links out. Tests shorten it.
var PCLinkStale = 35 * time.Second

// PCReplyTimeout is how long a request of gripi pc waits for the desktop app's reply. Tests shorten it.
var PCReplyTimeout = 5 * time.Minute

// PCPins is how many agent connections the gateway keeps on the links of their first messages. Tests lower it.
var PCPins = 1024

// pcMessageLimit is the size limit of an SSH agent message, as OpenSSH's agent has it.
const pcMessageLimit = 256 << 10

// pcPollMessages is the most messages that a poll carries, as the desktop app drops a link that sends more.
const pcPollMessages = 64

// pcPollBytes bounds the JSON of a poll's messages, which the desktop app reads only up to 8 MiB. A poll with one
// message may have more, up to about 6 MiB for a command whose words JSON escapes.
const pcPollBytes = 4 << 20

// pcSummaryLimit is the most characters of a request's summary that Gripi shows while a computer asks to approve it.
const pcSummaryLimit = 200

// pcRunWords and pcRunWordBytes bound a command of gripi pc run, as the desktop app drops a message with more.
const (
	pcRunWords     = 256
	pcRunWordBytes = 4096
)

// pcReplyBytes bounds the body of a reply over the link, which carries up to sudoOutputLimit of each output of a command
// in base64.
const pcReplyBytes = 48 << 20

var (
	errNoPCLink   = errors.New("no computer is connected; open the Gripi desktop app there and turn on File → Allow PC Access for This Server")
	errPCLinkLost = errors.New("the computer's link to Gripi ended before it replied")
	errPCTimeout  = errors.New("the computer did not reply in time")
	errPCTaken    = errors.New("another link of this browser is open, such as one of a desktop app that just restarted")
	errPCUnknown  = errors.New("the link is not open, such as after the gateway restarted")
	errPCEnded    = errors.New("the link ended")
	errPCUnused   = errors.New("nobody has used this session in a Gripi desktop app with PC access since the gateway started; ask the user to open it in the desktop app on the computer that should do this, and click in it")
	errPCNoAccess = errors.New("this session was last used in a Gripi window without PC access, such as a phone or a browser; ask the user to open it in the desktop app on the computer that should do this, and click in it")
	errPCMoved    = errors.New("the session was used on another computer during this SSH agent connection; run the command again")
	errPCNotOpen  = errors.New("this SSH agent connection didn't start on the computer that the session uses; run the command again")
	errPCReopened = errors.New("this SSH agent connection is already open")
	errPCPinned   = errors.New("too many SSH agent connections are open for PC access")
	errPCClosed   = errors.New("the SSH agent connection closed")
)

const (
	pcRequestGone = "The request no longer waits for approval."
	pcRunning     = "The command already runs."
)

// pcMessage is a message of the gateway to the desktop app.
type pcMessage struct {
	Type        string   `json:"type"`
	ID          string   `json:"id,omitempty"`
	Session     string   `json:"session,omitempty"`
	SessionName string   `json:"session_name,omitempty"`
	Connection  string   `json:"connection,omitempty"`
	Message     []byte   `json:"message,omitempty"`
	Argv        []string `json:"argv,omitempty"`
	CWD         string   `json:"cwd,omitempty"`
}

// pcLinkBody is what the desktop app sends on its link: a poll or a close names the link, and a reply also a request
// and its message, result or error. A link's first poll opens it, and names the computer. A status says whether the app
// asks the user to approve a request, and while it does, what for, and for how many seconds more, or that it is about
// to run a command that the user approved.
type pcLinkBody struct {
	Link      string      `json:"link"`
	Open      bool        `json:"open"`
	Name      string      `json:"name"`
	ID        string      `json:"id"`
	Message   []byte      `json:"message"`
	Result    *SudoResult `json:"result"`
	Error     string      `json:"error"`
	Asking    bool        `json:"asking"`
	Summary   string      `json:"summary"`
	ExpiresIn float64     `json:"expires_in"`
	Starting  bool        `json:"starting"`
}

// pcLink is a desktop app's link, which lives while it polls.
type pcLink struct {
	id string
	// owner is the browser that opened the link, as pushOwner gives it.
	owner string
	polls int
	// seen is when its last poll ended.
	seen  time.Time
	queue []pcMessage
	// queued closes once the queue has messages.
	queued chan struct{}
	// gone closes when the link ends, which fails the requests that wait for its replies.
	gone    chan struct{}
	waiting map[string]*pcWaiter
}

// pcWaiter is a request that waits for the desktop app's reply, and the approval that the app may ask the user for
// meanwhile, which every Gripi client of the request's session shows.
type pcWaiter struct {
	// ctx is gripi's request, which no longer waits once it ends.
	ctx     context.Context
	replied chan pcLinkBody
	// timeout fires when the app didn't reply in time, but not once a command that it was about to run started.
	timeout  *time.Timer
	approval pcApproval
	// asking is while the app asks, and asked once it began or stopped, as it asks only once: a start that comes after
	// the stop, which it may overtake, doesn't show it again. started is once a command may run, which Cancel in Gripi
	// can then no longer stop.
	asking, asked, started bool
}

// pcApproval is a request that a computer asks the user to approve.
type pcApproval struct {
	ID        string    `json:"id"`
	Computer  string    `json:"computer"`
	Summary   string    `json:"summary"`
	ExpiresAt time.Time `json:"expires_at"`
	session   string
	// kind is the type of the request's message.
	kind string
}

// pcLinks holds the links of the desktop apps, at most one for each approved browser, and what routes the requests of
// gripi pc to them, only in memory.
type pcLinks struct {
	mu   sync.Mutex
	byID map[string]*pcLink
	// names are the computers' names by owner, as their links gave them, also after the links ended. A link that gave none
	// leaves an empty one.
	names map[string]string
	// uses are the latest use of each session, by its canonical key.
	uses map[string]pcUse
	// pins keep each agent connection on the link of its first message, by the connection's ID.
	pins map[string]pcPin
	// stopped refuses links once the gateway stops.
	stopped bool
}

// pcUse is when the user last used a session, and in which browser.
type pcUse struct {
	owner string
	at    time.Time
}

// pcPin is the link of an agent connection, and the session key that its first message had, which the desktop app
// tells the connection apart by.
type pcPin struct {
	link    *pcLink
	session string
}

// send queues a message for the link's next poll. Callers hold mu.
func (link *pcLink) send(message pcMessage) {
	if len(link.queue) == 0 {
		close(link.queued)
	}
	link.queue = append(link.queue, message)
}

// live reports whether a link hasn't ended. Callers hold mu.
func (links *pcLinks) live(link *pcLink) bool {
	return links.byID[link.id] == link
}

// end releases a link that hasn't ended. Callers hold mu.
func (links *pcLinks) end(link *pcLink) {
	if links.live(link) {
		delete(links.byID, link.id)
		close(link.gone)
	}
}

// poll waits up to PCLinkPoll for messages to the link of owner that body names. A poll that opens a link opens it
// unless another link of owner is open. Other polls of a link that isn't open, such as one from before a restart, or
// that another owner opened, such as before the browser's cookie changed, are refused, so that the desktop app drops
// what it allowed on it.
func (links *pcLinks) poll(ctx context.Context, owner string, body pcLinkBody) ([]pcMessage, error) {
	links.mu.Lock()
	if links.stopped {
		links.mu.Unlock()
		return nil, errPCEnded
	}
	link := links.byID[body.Link]
	switch {
	case link != nil && link.owner != owner, link == nil && !body.Open:
		links.mu.Unlock()
		return nil, errPCUnknown
	case link == nil && links.linkOf(owner) != nil:
		links.mu.Unlock()
		return nil, errPCTaken
	case link == nil:
		link = &pcLink{id: body.Link, owner: owner, queued: make(chan struct{}), gone: make(chan struct{}), waiting: make(map[string]*pcWaiter)}
		if links.byID == nil {
			links.byID, links.names = make(map[string]*pcLink), make(map[string]string)
		}
		links.byID[link.id] = link
		links.names[owner] = body.Name
	}
	link.polls++
	queued := link.queued
	links.mu.Unlock()

	timeout := time.NewTimer(PCLinkPoll)
	defer timeout.Stop()
	select {
	case <-queued:
	case <-link.gone:
	case <-timeout.C:
	case <-ctx.Done():
	}
	links.mu.Lock()
	defer links.mu.Unlock()
	link.polls--
	if !links.live(link) {
		return nil, errPCEnded
	}
	link.seen = time.Now()
	stale := PCLinkStale
	time.AfterFunc(stale, func() {
		links.mu.Lock()
		defer links.mu.Unlock()
		if link.polls == 0 && time.Since(link.seen) >= stale {
			links.end(link)
		}
	})
	// The messages of a poll that the app no longer reads wait for its next one.
	if len(link.queue) == 0 || ctx.Err() != nil {
		return nil, nil
	}
	count, size := 0, 0
	for count < min(len(link.queue), pcPollMessages) {
		encoded, _ := json.Marshal(link.queue[count])
		if size += len(encoded); count > 0 && size > pcPollBytes {
			break
		}
		count++
	}
	messages := link.queue[:count]
	link.queue = link.queue[count:]
	if len(link.queue) == 0 {
		link.queue, link.queued = nil, make(chan struct{})
	}
	return messages, nil
}

// linkOf returns the link of owner, if it has one. Callers hold mu.
func (links *pcLinks) linkOf(owner string) *pcLink {
	for _, link := range links.byID {
		if link.owner == owner {
			return link
		}
	}
	return nil
}

// linked reports whether owner has a link.
func (links *pcLinks) linked(owner string) bool {
	links.mu.Lock()
	defer links.mu.Unlock()
	return links.linkOf(owner) != nil
}

// use records that owner used a session at the time at, unless a later use is on record. resolve gives the session's
// key while remapUse waits, so that a use doesn't stay behind on a key that moved.
func (links *pcLinks) use(owner string, at time.Time, resolve func() string) {
	links.mu.Lock()
	defer links.mu.Unlock()
	session := resolve()
	if links.uses[session].at.After(at) {
		return
	}
	if links.uses == nil {
		links.uses = make(map[string]pcUse)
	}
	links.uses[session] = pcUse{owner: owner, at: at}
}

// remapUse moves the use of a new session to the key that it has once Pi saved it, in the same step as remap, which
// moves the session there, and keeps the newer one when both have one. The requests that wait in it move too, as Gripi
// shows them by that key, while their messages keep the key that the desktop app knows.
func (links *pcLinks) remapUse(from, to string, remap func()) {
	links.mu.Lock()
	defer links.mu.Unlock()
	remap()
	if use, used := links.uses[from]; used {
		delete(links.uses, from)
		if use.at.After(links.uses[to].at) {
			links.uses[to] = use
		}
	}
	for _, link := range links.byID {
		for _, waiter := range link.waiting {
			if waiter.approval.session == from {
				waiter.approval.session = to
			}
		}
	}
}

// route finds the link for a command, or a message of an agent connection, in session, whose parents are by session
// key, and returns it with the session key to send. The link is that of the browser where session was last used, or
// else the nearest of its parents. A connection stays on the link of its first message, which opens it, so that the
// desktop app sees all its messages, and with that message's session, which resolve finds where a new one moved. A
// request never goes to another link.
func (links *pcLinks) route(parents map[string]string, session, connection string, open bool, resolve func(string) string) (*pcLink, string, error) {
	links.mu.Lock()
	defer links.mu.Unlock()
	pin, pinned := links.pins[connection]
	if pinned {
		session = resolve(pin.session)
	}
	use, used := links.uses[session]
	for key, step := session, 0; !used && step < 8; step++ {
		if key = parents[key]; key == "" {
			break
		}
		use, used = links.uses[key]
	}
	link := links.linkOf(use.owner)
	if !used || link == nil {
		name, linked := links.names[use.owner]
		switch {
		case !used && len(links.byID) == 0:
			return nil, "", errNoPCLink
		case !used:
			return nil, "", errPCUnused
		case !linked:
			return nil, "", errPCNoAccess
		}
		return nil, "", fmt.Errorf("this session was last used on %s, where the Gripi desktop app isn't connected for PC access; ask the user to open the app there, or to open the session in the desktop app on the computer that should do this, and click in it", cmp.Or(name, "the user's computer"))
	}
	// A command has no connection to keep on the link.
	if connection == "" {
		return link, session, nil
	}
	switch {
	case pinned && open:
		return nil, "", errPCReopened
	case pinned && !links.live(pin.link):
		return nil, "", errPCLinkLost
	case pinned && pin.link != link:
		return nil, "", errPCMoved
	case pinned:
		return link, pin.session, nil
	case !open:
		return nil, "", errPCNotOpen
	}
	// The pins of links that ended, which their connections' messages fail on, go once there are too many. Others may
	// still wait for replies.
	for key, pin := range links.pins {
		if len(links.pins) >= PCPins && !links.live(pin.link) {
			delete(links.pins, key)
		}
	}
	if len(links.pins) >= PCPins {
		return nil, "", errPCPinned
	}
	if links.pins == nil {
		links.pins = make(map[string]pcPin)
	}
	links.pins[connection] = pcPin{link: link, session: session}
	return link, session, nil
}

// ask queues a request of an agent connection, or a command, in a session for link and waits for its reply. When the
// reply doesn't come in time, or ctx ends first, the link gets a cancel of the request. session gives the session's key
// while remapUse waits, so that Gripi shows the request by the key that the session has, also when Pi saved a new one
// just now.
func (links *pcLinks) ask(ctx context.Context, link *pcLink, session func() string, message pcMessage) (pcLinkBody, error) {
	links.mu.Lock()
	if !links.live(link) {
		links.mu.Unlock()
		return pcLinkBody{}, errPCLinkLost
	}
	// A request after its connection's close, which may overtake it, would open the connection again on the link.
	if message.Type == "ssh" && links.pins[message.Connection].link != link {
		links.mu.Unlock()
		return pcLinkBody{}, errPCClosed
	}
	replied := make(chan pcLinkBody, 1)
	waiter := &pcWaiter{ctx: ctx, replied: replied, timeout: time.NewTimer(PCReplyTimeout), approval: pcApproval{ID: message.ID, session: session(), kind: message.Type}}
	defer waiter.timeout.Stop()
	link.waiting[message.ID] = waiter
	link.send(message)
	links.mu.Unlock()

	select {
	case reply := <-replied:
		return reply, nil
	case <-link.gone:
	case <-waiter.timeout.C:
	case <-ctx.Done():
	}
	links.mu.Lock()
	defer links.mu.Unlock()
	delete(link.waiting, message.ID)
	// A reply that came at the same moment wins.
	select {
	case reply := <-replied:
		return reply, nil
	default:
	}
	if !links.live(link) {
		return pcLinkBody{}, errPCLinkLost
	}
	link.send(pcMessage{Type: "cancel", ID: message.ID})
	return pcLinkBody{}, errPCTimeout
}

// reply hands a reply over a link of owner to the request that waits for it, and returns the status of the desktop
// app's request.
func (links *pcLinks) reply(owner string, reply pcLinkBody) int {
	links.mu.Lock()
	defer links.mu.Unlock()
	link := links.byID[reply.Link]
	if link == nil || link.owner != owner {
		return http.StatusConflict
	}
	waiter, waiting := link.waiting[reply.ID]
	if !waiting {
		return http.StatusNotFound
	}
	delete(link.waiting, reply.ID)
	waiter.replied <- reply
	return http.StatusOK
}

// status records whether the desktop app asks the user to approve a request that waits over a link of owner, and
// returns the status of the app's request, and the approval once the app begins to ask. The app's summary is escaped
// and cut, as it shows in Gripi.
func (links *pcLinks) status(owner string, status pcLinkBody) (int, *pcApproval) {
	links.mu.Lock()
	defer links.mu.Unlock()
	link := links.byID[status.Link]
	if link == nil || link.owner != owner {
		return http.StatusConflict, nil
	}
	waiter, waiting := link.waiting[status.ID]
	switch {
	case !waiting:
		return http.StatusNotFound, nil
	case status.Starting:
		// The app runs the command only on this answer, so it is no once gripi stopped waiting, also before ask took the
		// request away. A command that runs has no timeout or Cancel.
		if waiter.approval.kind != "run" || waiter.ctx.Err() != nil || !waiter.timeout.Stop() {
			return http.StatusNotFound, nil
		}
		waiter.asking, waiter.asked, waiter.started = false, true, true
		return http.StatusOK, nil
	case !status.Asking:
		waiter.asking, waiter.asked = false, true
		return http.StatusOK, nil
	case waiter.asked:
		return http.StatusOK, nil
	}
	var summary []rune
	for _, char := range status.Summary {
		if len(summary) > pcSummaryLimit {
			break
		}
		if unicode.In(char, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) {
			summary = append(summary, []rune(fmt.Sprintf(`\u%04x`, char))...)
		} else {
			summary = append(summary, char)
		}
	}
	if len(summary) > pcSummaryLimit {
		summary = append(summary[:pcSummaryLimit-1], '…')
	}
	expiresIn := min(max(status.ExpiresIn, 1), PCReplyTimeout.Seconds())
	waiter.approval.Computer, waiter.approval.Summary = links.names[owner], string(summary)
	waiter.approval.ExpiresAt = time.Now().Add(time.Duration(expiresIn * float64(time.Second)))
	waiter.asking, waiter.asked = true, true
	approval := waiter.approval
	return http.StatusOK, &approval
}

// approvals returns the requests in session that computers ask the user to approve, the oldest first.
func (links *pcLinks) approvals(session string) []pcApproval {
	links.mu.Lock()
	defer links.mu.Unlock()
	approvals := []pcApproval{}
	for _, link := range links.byID {
		for _, waiter := range link.waiting {
			if waiter.asking && waiter.approval.session == session {
				approvals = append(approvals, waiter.approval)
			}
		}
	}
	slices.SortFunc(approvals, func(left, right pcApproval) int { return left.ExpiresAt.Compare(right.ExpiresAt) })
	return approvals
}

// cancel refuses a request that a computer asks the user to approve, if allowed accepts its session, and closes the
// computer's dialog. It acts only while the computer asks, as once it stopped, the user may have approved it, and
// otherwise says why it didn't.
func (links *pcLinks) cancel(id string, allowed func(session string) bool) (refusal string) {
	links.mu.Lock()
	defer links.mu.Unlock()
	for _, link := range links.byID {
		waiter := link.waiting[id]
		switch {
		case waiter == nil || !allowed(waiter.approval.session):
			continue
		case waiter.started:
			return pcRunning
		case !waiter.asking:
			return pcRequestGone
		}
		delete(link.waiting, id)
		waiter.replied <- pcLinkBody{Error: "cancelled in Gripi"}
		link.send(pcMessage{Type: "cancel", ID: id})
		return ""
	}
	return pcRequestGone
}

// closeConnection tells the link of an agent connection that it closed, and forgets the connection.
func (links *pcLinks) closeConnection(connection string) {
	links.mu.Lock()
	defer links.mu.Unlock()
	pin, pinned := links.pins[connection]
	if !pinned {
		return
	}
	delete(links.pins, connection)
	if links.live(pin.link) {
		pin.link.send(pcMessage{Type: "ssh_close", Session: pin.session, Connection: connection})
	}
}

// endOwner ends the link of owner, if it has one.
func (links *pcLinks) endOwner(owner string) {
	links.mu.Lock()
	defer links.mu.Unlock()
	if link := links.linkOf(owner); link != nil {
		links.end(link)
	}
}

func (links *pcLinks) release(owner, id string) {
	links.mu.Lock()
	defer links.mu.Unlock()
	if link := links.byID[id]; link != nil && link.owner == owner {
		links.end(link)
	}
}

// EndPCLink ends the desktop apps' links, as their closes do, and refuses new ones, so that neither their polls nor the
// requests that wait for their replies hold the gateway's shutdown.
func (handler *Handler) EndPCLink() {
	links := &handler.app.pcLinks
	links.mu.Lock()
	defer links.mu.Unlock()
	links.stopped = true
	for _, link := range links.byID {
		links.end(link)
	}
}

func (app *application) registerPCRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /pc/link", app.pollPCLink)
	mux.HandleFunc("POST /pc/link/reply", app.replyOverPCLink)
	mux.HandleFunc("POST /pc/link/close", app.closePCLink)
	mux.HandleFunc("POST /pc/link/status", app.pcLinkStatus)
	mux.HandleFunc("POST /pc/cancel", app.cancelPCApproval)
}

// pcAccessRefusal says why the gateway refuses PC access, or is empty when it allows it.
func (app *application) pcAccessRefusal() string {
	switch {
	case app.config.MultiUserMode:
		return "PC access is off in multi-user mode, where the agents of all users share one OS user"
	case !app.browserAccessEnabled():
		return "PC access needs access approval, which is disabled on this gateway"
	}
	return ""
}

// readPCLinkRequest reads the body of a request of the desktop app's link, of up to limit bytes, and the browser that
// sends it. Pages send an Origin or Sec-Fetch-* header with every POST, and can't send the link's header across origins
// without CORS, so no page can play the link. Plain HTTP from another machine would let anyone on the way slip a request
// into a poll.
func (app *application) readPCLinkRequest(response http.ResponseWriter, request *http.Request, limit int64) (pcLinkBody, string, bool) {
	var body pcLinkBody
	fetchMetadata := false
	for name := range request.Header {
		fetchMetadata = fetchMetadata || strings.HasPrefix(strings.ToLower(name), "sec-fetch-")
	}
	_, origin := request.Header["Origin"]
	mediaType, _, _ := mime.ParseMediaType(request.Header.Get("Content-Type"))
	refusal := app.pcAccessRefusal()
	if refusal == "" && (origin || fetchMetadata || request.Header.Get("X-Gripi-PC-Link") != "1" || mediaType != "application/json") {
		refusal = "only the Gripi desktop app may use the PC link"
	}
	if refusal == "" && !app.secureOrLocalTransport(request) {
		refusal = "the PC link needs HTTPS"
	}
	owner, ok := app.pushOwner(request)
	if refusal == "" && !ok {
		refusal = "only an approved browser may use the PC link"
	}
	if refusal != "" {
		writeJSONStatus(response, http.StatusForbidden, map[string]string{"error": refusal})
		return body, "", false
	}
	request.Body = http.MaxBytesReader(response, request.Body, limit)
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.Link == "" {
		writeJSONStatus(response, http.StatusBadRequest, map[string]string{"error": "send a JSON object that names the link"})
		return body, "", false
	}
	return body, owner, true
}

func (app *application) pollPCLink(response http.ResponseWriter, request *http.Request) {
	body, owner, ok := app.readPCLinkRequest(response, request, 1<<20)
	if !ok {
		return
	}
	if utf8.RuneCountInString(body.Name) > 64 || strings.ContainsFunc(body.Name, func(char rune) bool { return !unicode.IsGraphic(char) }) {
		writeJSONStatus(response, http.StatusBadRequest, map[string]string{"error": "the computer's name is a line of up to 64 characters"})
		return
	}
	messages, err := app.pcLinks.poll(request.Context(), owner, body)
	switch err {
	case errPCTaken, errPCUnknown:
		writeJSONStatus(response, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	case errPCEnded:
		writeJSONStatus(response, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	if messages == nil {
		messages = []pcMessage{}
	}
	writeJSON(response, map[string][]pcMessage{"messages": messages})
}

func (app *application) replyOverPCLink(response http.ResponseWriter, request *http.Request) {
	body, owner, ok := app.readPCLinkRequest(response, request, pcReplyBytes)
	if !ok {
		return
	}
	result := body.Result
	if body.ID == "" || body.Error == "" && len(body.Message) == 0 && result == nil || len(body.Message) > pcMessageLimit || result != nil && (len(result.Stdout) > sudoOutputLimit || len(result.Stderr) > sudoOutputLimit) {
		writeJSONStatus(response, http.StatusBadRequest, map[string]string{"error": "a reply needs the request's id, and a message of up to 256 KiB, a result with up to 16 MiB of each output, or an error"})
		return
	}
	switch status := app.pcLinks.reply(owner, body); status {
	case http.StatusConflict:
		writeJSONStatus(response, status, map[string]string{"error": "the link is not open"})
	case http.StatusNotFound:
		writeJSONStatus(response, status, map[string]string{"error": "the request no longer waits for a reply"})
	default:
		writeJSON(response, struct{}{})
	}
}

// pcLinkStatus takes the desktop app's word that it asks the user to approve a request, or that it stopped asking, or
// answers whether it may run a command that the user approved.
func (app *application) pcLinkStatus(response http.ResponseWriter, request *http.Request) {
	body, owner, ok := app.readPCLinkRequest(response, request, 1<<20)
	if !ok {
		return
	}
	if body.ID == "" || body.Asking && strings.TrimSpace(body.Summary) == "" {
		writeJSONStatus(response, http.StatusBadRequest, map[string]string{"error": "a status needs the request's id, and while asking, a summary"})
		return
	}
	status, began := app.pcLinks.status(owner, body)
	switch status {
	case http.StatusConflict:
		writeJSONStatus(response, status, map[string]string{"error": "the link is not open"})
	case http.StatusNotFound:
		writeJSONStatus(response, status, map[string]string{"error": "the request no longer waits for a reply"})
	default:
		writeJSON(response, struct{}{})
	}
	if began != nil {
		go app.notifyPCApproval(owner, *began)
	}
}

// notifyPCApproval tells the user at once that a computer asks them to approve a request, on each of their browsers
// but asker, the one of that computer, whose desktop app has a notification of its own. Unlike notifySudo, it goes out also
// while a client shows the session: in single-user mode, all are one for presence, also the computer that asks, which
// comes to the front. The service worker of a device that shows the session skips it instead.
func (app *application) notifyPCApproval(asker string, approval pcApproval) {
	owners, err := app.completionNotifications.owners(approval.session)
	if err != nil {
		log.Printf("find whom to notify of a PC access request: %v", err)
		return
	}
	owners = slices.DeleteFunc(owners, func(owner string) bool { return owner == asker })
	// A command may hold secrets, which a locked screen would show.
	summary := approval.Summary
	if approval.kind == "run" {
		summary = "run a command"
	}
	app.notifyAtOnce(owners, approval.session, approval.ID, "pc", "Approve on "+cmp.Or(approval.Computer, "your computer")+": "+summary)
}

// cancelPCApproval refuses, from any Gripi client of its session, a request that a computer asks the user to approve,
// so that gripi pc needn't wait for the computer.
func (app *application) cancelPCApproval(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	owned := func(session string) bool { return app.validOwnedSessionPath(request, session) }
	if refusal := app.pcLinks.cancel(request.PostFormValue("id"), owned); refusal != "" {
		writeJSONStatus(response, http.StatusConflict, map[string]string{"error": refusal})
		return
	}
	writeJSON(response, map[string]bool{"ok": true})
}

func (app *application) closePCLink(response http.ResponseWriter, request *http.Request) {
	body, owner, ok := app.readPCLinkRequest(response, request, 1<<20)
	if !ok {
		return
	}
	app.pcLinks.release(owner, body.Link)
	writeJSON(response, struct{}{})
}

// usedPC records that the browser of a request used a session, as the page names it, when the gateway received the
// request, so that the requests of gripi pc in the session go to that browser's computer.
func (app *application) usedPC(request *http.Request, session string, received time.Time) {
	if app.pcAccessRefusal() != "" {
		return
	}
	owner, ok := app.pushOwner(request)
	path, valid := app.sessionStore.Canonical(session)
	if !ok || !valid || app.scratch.contains(path) || !app.validOwnedSessionPath(request, path) || !app.knownOrPendingSession(request, path) {
		return
	}
	app.pcLinks.use(owner, received, func() string {
		resolved, _, _ := app.resolveOwnedPendingPath(request, path)
		return resolved
	})
}

// pcSSHAgent relays a message of an SSH agent connection of gripi pc ssh to the computer where the user last used the
// session, and returns its reply, or reports that the connection closed.
func (app *application) pcSSHAgent(response http.ResponseWriter, request *http.Request) {
	if refusal := app.pcAccessRefusal(); refusal != "" {
		writeJSONStatus(response, http.StatusForbidden, map[string]string{"error": refusal})
		return
	}
	if !parseForm(response, request) {
		return
	}
	session, connection, closing, open := request.PostFormValue("session"), request.PostFormValue("connection"), request.PostFormValue("close") == "1", request.PostFormValue("open") == "1"
	message, err := base64.StdEncoding.DecodeString(request.PostFormValue("message"))
	if connection == "" || len(connection) > 64 || !closing && (err != nil || len(message) == 0 || len(message) > pcMessageLimit) {
		writeJSONStatus(response, http.StatusBadRequest, map[string]string{"error": "an agent request needs a connection ID of up to 64 characters, and a message of up to 256 KiB in base64 or close=1"})
		return
	}
	session, ok := app.pcSession(response, request, session)
	if !ok {
		return
	}
	if closing {
		app.pcLinks.closeConnection(connection)
		writeJSON(response, struct{}{})
		return
	}
	reply, ok := app.askPC(response, request, session, open, pcMessage{Type: "ssh", Connection: connection, Message: message})
	if ok {
		writeJSON(response, map[string][]byte{"message": reply.Message})
	}
}

// pcRun asks the computer where the user last used the session to run a command of gripi pc run, and returns how it
// ended.
func (app *application) pcRun(response http.ResponseWriter, request *http.Request) {
	if refusal := app.pcAccessRefusal(); refusal != "" {
		writeJSONStatus(response, http.StatusForbidden, map[string]string{"error": refusal})
		return
	}
	if !parseForm(response, request) {
		return
	}
	argv, cwd := request.PostForm["argv"], request.PostFormValue("cwd")
	// The desktop app drops a message that it couldn't show or run as it is, which would leave gripi waiting.
	valid := len(argv) > 0 && len(argv) <= pcRunWords && argv[0] != "" && (cwd == "" || filepath.IsAbs(cwd))
	for _, word := range slices.Concat(argv, []string{cwd}) {
		valid = valid && len(word) <= pcRunWordBytes && utf8.ValidString(word) && !strings.ContainsRune(word, 0)
	}
	if !valid {
		writeJSONStatus(response, http.StatusBadRequest, map[string]string{"error": "a command needs 1 to 256 words of up to 4096 bytes of text without NUL, the first not empty, and an absolute folder or none"})
		return
	}
	session, ok := app.pcSession(response, request, request.PostFormValue("session"))
	if !ok {
		return
	}
	reply, ok := app.askPC(response, request, session, false, pcMessage{Type: "run", Argv: argv, CWD: cwd})
	switch {
	case !ok:
	case reply.Result == nil:
		writeJSONStatus(response, http.StatusBadGateway, map[string]string{"error": "the computer replied without the command's result"})
	default:
		writeJSON(response, reply.Result)
	}
}

// pcSession returns the canonical key of the session of a request of gripi pc, which must be one of the user's that
// the gateway knows, and on a worker, one that runs there, or writes why it isn't.
func (app *application) pcSession(response http.ResponseWriter, request *http.Request, session string) (string, bool) {
	if !app.validOwnedSessionPath(request, session) || !app.knownOrPendingSession(request, session) {
		writeJSONStatus(response, http.StatusNotFound, map[string]string{"error": "the command runs in no session of yours in Gripi"})
		return "", false
	}
	// The desktop app tells the sessions apart by their canonical paths, which an alias would split.
	session, err := app.canonicalRPCSessionPath(request, session)
	if err != nil {
		http.Error(response, "Unable to remap pending session", http.StatusInternalServerError)
		return "", false
	}
	return session, true
}

// askPC sends a request of gripi pc in session, which open marks as an agent connection's first message, to the
// computer that route finds, and returns the desktop app's reply, or writes why there is none.
func (app *application) askPC(response http.ResponseWriter, request *http.Request, session string, open bool, message pcMessage) (pcLinkBody, bool) {
	parents, err := app.gatewayState.SessionParents()
	if err != nil {
		writeInternalError(response, "read session parents", err)
		return pcLinkBody{}, false
	}
	resolve := func(key string) string {
		resolved, _, _ := app.resolveOwnedPendingPath(request, key)
		return resolved
	}
	link, key, err := app.pcLinks.route(parents, session, message.Connection, open, resolve)
	if err != nil {
		writeJSONStatus(response, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return pcLinkBody{}, false
	}
	if message.ID, err = randomBrowserToken(); err != nil {
		writeInternalError(response, "create a PC request ID", err)
		return pcLinkBody{}, false
	}
	message.Session = key
	if found, ok := app.sessionStore.Session(session); ok {
		message.SessionName = agent.NotificationPreview(found.DisplayName)
	}
	reply, err := app.pcLinks.ask(request.Context(), link, func() string { return resolve(session) }, message)
	switch {
	case request.Context().Err() != nil:
	case errors.Is(err, errPCTimeout):
		writeJSONStatus(response, http.StatusGatewayTimeout, map[string]string{"error": err.Error()})
	case err != nil:
		writeJSONStatus(response, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
	case reply.Error != "":
		writeJSONStatus(response, http.StatusBadGateway, map[string]string{"error": reply.Error})
	default:
		return reply, true
	}
	return pcLinkBody{}, false
}
