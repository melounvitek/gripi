package server

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
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

// pcPollBytes bounds the agent messages of a poll, whose JSON the desktop app reads only up to 8 MiB.
const pcPollBytes = 2 << 20

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
)

// pcMessage is a message of the gateway to the desktop app.
type pcMessage struct {
	Type        string `json:"type"`
	ID          string `json:"id,omitempty"`
	Session     string `json:"session,omitempty"`
	SessionName string `json:"session_name,omitempty"`
	Connection  string `json:"connection,omitempty"`
	Message     []byte `json:"message,omitempty"`
}

// pcLinkBody is what the desktop app sends on its link: a poll or a close names the link, and a reply also a request
// and its message or error. A link's first poll opens it, and names the computer.
type pcLinkBody struct {
	Link    string `json:"link"`
	Open    bool   `json:"open"`
	Name    string `json:"name"`
	ID      string `json:"id"`
	Message []byte `json:"message"`
	Error   string `json:"error"`
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
	waiting map[string]chan pcLinkBody
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
		link = &pcLink{id: body.Link, owner: owner, queued: make(chan struct{}), gone: make(chan struct{}), waiting: make(map[string]chan pcLinkBody)}
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
		if size += len(link.queue[count].Message); count > 0 && size > pcPollBytes {
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
// moves the session there, and keeps the newer one when both have one.
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
}

// route finds the link for a message of an agent connection in session, whose parents are by session key, and returns
// it with the session key to send. The link is that of the browser where session was last used, or else the nearest
// of its parents. A connection stays on the link of its first message, which opens it, so that the desktop app sees
// all its messages, and with that message's session, which resolve finds where a new one moved. A request never goes
// to another link.
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

// ask queues a request for link and waits for its reply. When the reply doesn't come in time, or ctx ends first, the
// link gets a cancel of the request.
func (links *pcLinks) ask(ctx context.Context, link *pcLink, message pcMessage) (pcLinkBody, error) {
	links.mu.Lock()
	if !links.live(link) {
		links.mu.Unlock()
		return pcLinkBody{}, errPCLinkLost
	}
	replied := make(chan pcLinkBody, 1)
	link.waiting[message.ID] = replied
	link.send(message)
	links.mu.Unlock()

	timeout := time.NewTimer(PCReplyTimeout)
	defer timeout.Stop()
	select {
	case reply := <-replied:
		return reply, nil
	case <-link.gone:
	case <-timeout.C:
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
	replied, waiting := link.waiting[reply.ID]
	if !waiting {
		return http.StatusNotFound
	}
	delete(link.waiting, reply.ID)
	replied <- reply
	return http.StatusOK
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

// readPCLinkRequest reads the body of a request of the desktop app's link, and the browser that sends it. Pages send an
// Origin or Sec-Fetch-* header with every POST, and can't send the link's header across origins without CORS, so no
// page can play the link. Plain HTTP from another machine would let anyone on the way slip a request into a poll.
func (app *application) readPCLinkRequest(response http.ResponseWriter, request *http.Request) (pcLinkBody, string, bool) {
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
	request.Body = http.MaxBytesReader(response, request.Body, 1<<20)
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.Link == "" {
		writeJSONStatus(response, http.StatusBadRequest, map[string]string{"error": "send a JSON object that names the link"})
		return body, "", false
	}
	return body, owner, true
}

func (app *application) pollPCLink(response http.ResponseWriter, request *http.Request) {
	body, owner, ok := app.readPCLinkRequest(response, request)
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
	body, owner, ok := app.readPCLinkRequest(response, request)
	if !ok {
		return
	}
	if body.ID == "" || body.Error == "" && len(body.Message) == 0 || len(body.Message) > pcMessageLimit {
		writeJSONStatus(response, http.StatusBadRequest, map[string]string{"error": "a reply needs the request's id, and a message of up to 256 KiB or an error"})
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

func (app *application) closePCLink(response http.ResponseWriter, request *http.Request) {
	body, owner, ok := app.readPCLinkRequest(response, request)
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
	if !app.validOwnedSessionPath(request, session) || !app.knownOrPendingSession(request, session) {
		writeJSONStatus(response, http.StatusNotFound, map[string]string{"error": "the command runs in no session of yours in Gripi"})
		return
	}
	// The desktop app tells the sessions apart by their canonical paths, which an alias would split.
	session, err = app.canonicalRPCSessionPath(request, session)
	if err != nil {
		http.Error(response, "Unable to remap pending session", http.StatusInternalServerError)
		return
	}
	if closing {
		app.pcLinks.closeConnection(connection)
		writeJSON(response, struct{}{})
		return
	}
	parents, err := app.gatewayState.SessionParents()
	if err != nil {
		writeInternalError(response, "read session parents", err)
		return
	}
	link, key, err := app.pcLinks.route(parents, session, connection, open, func(pinned string) string {
		resolved, _, _ := app.resolveOwnedPendingPath(request, pinned)
		return resolved
	})
	if err != nil {
		writeJSONStatus(response, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	id, err := randomBrowserToken()
	if err != nil {
		writeInternalError(response, "create a PC request ID", err)
		return
	}
	name := ""
	if found, ok := app.sessionStore.Session(session); ok {
		name = agent.NotificationPreview(found.DisplayName)
	}
	reply, err := app.pcLinks.ask(request.Context(), link, pcMessage{Type: "ssh", ID: id, Session: key, SessionName: name, Connection: connection, Message: message})
	switch {
	case request.Context().Err() != nil:
	case errors.Is(err, errPCTimeout):
		writeJSONStatus(response, http.StatusGatewayTimeout, map[string]string{"error": err.Error()})
	case err != nil:
		writeJSONStatus(response, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
	case reply.Error != "":
		writeJSONStatus(response, http.StatusBadGateway, map[string]string{"error": reply.Error})
	default:
		writeJSON(response, map[string][]byte{"message": reply.Message})
	}
}
