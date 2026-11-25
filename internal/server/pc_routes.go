package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
)

// PCLinkPoll is how long a poll of the desktop app's link waits for messages. Tests shorten it.
var PCLinkPoll = 25 * time.Second

// PCLinkStale is how long after its last poll a link is lost, and until then keeps other links out. Tests shorten it.
var PCLinkStale = 35 * time.Second

// PCReplyTimeout is how long a request of gripi pc waits for the desktop app's reply. Tests shorten it.
var PCReplyTimeout = 5 * time.Minute

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
	errPCTaken    = errors.New("another Gripi desktop app is connected for PC access")
	errPCUnknown  = errors.New("the link is not open, such as after the gateway restarted")
	errPCEnded    = errors.New("the link ended")
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
// and its message or error. A link's first poll opens it.
type pcLinkBody struct {
	Link    string `json:"link"`
	Open    bool   `json:"open"`
	ID      string `json:"id"`
	Message []byte `json:"message"`
	Error   string `json:"error"`
}

// pcLink is a desktop app's link, which lives while it polls.
type pcLink struct {
	id    string
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

// pcLinks holds the one link that relays the requests of gripi pc, only in memory.
type pcLinks struct {
	mu      sync.Mutex
	current *pcLink
	// stopped refuses links once the gateway stops.
	stopped bool
}

// send queues a message for the link's next poll. Callers hold mu.
func (link *pcLink) send(message pcMessage) {
	if len(link.queue) == 0 {
		close(link.queued)
	}
	link.queue = append(link.queue, message)
}

// end releases a link that is the current one. Callers hold mu.
func (links *pcLinks) end(link *pcLink) {
	if links.current == link {
		links.current = nil
		close(link.gone)
	}
}

// poll waits up to PCLinkPoll for messages to the current link with id. A poll that opens a link makes it the current
// one unless another one is. Other polls of a link that isn't the current one, such as one from before a restart, are
// refused, so that the desktop app drops what it allowed on it.
func (links *pcLinks) poll(ctx context.Context, id string, open bool) ([]pcMessage, error) {
	links.mu.Lock()
	if links.stopped {
		links.mu.Unlock()
		return nil, errPCEnded
	}
	link := links.current
	switch {
	case link == nil && open:
		link = &pcLink{id: id, queued: make(chan struct{}), gone: make(chan struct{}), waiting: make(map[string]chan pcLinkBody)}
		links.current = link
	case link == nil:
		links.mu.Unlock()
		return nil, errPCUnknown
	case link.id != id:
		links.mu.Unlock()
		return nil, errPCTaken
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
	if links.current != link {
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

// ask queues a request for the current link and waits for its reply. When the reply doesn't come in time, or ctx
// ends first, the link gets a cancel of the request.
func (links *pcLinks) ask(ctx context.Context, message pcMessage) (pcLinkBody, error) {
	links.mu.Lock()
	link := links.current
	if link == nil {
		links.mu.Unlock()
		return pcLinkBody{}, errNoPCLink
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
	if links.current != link {
		return pcLinkBody{}, errPCLinkLost
	}
	link.send(pcMessage{Type: "cancel", ID: message.ID})
	return pcLinkBody{}, errPCTimeout
}

// reply hands a reply to the request that waits for it, and returns the status of the desktop app's request.
func (links *pcLinks) reply(reply pcLinkBody) int {
	links.mu.Lock()
	defer links.mu.Unlock()
	link := links.current
	if link == nil || link.id != reply.Link {
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

// notify queues a message that needs no reply for the current link, if there is one.
func (links *pcLinks) notify(message pcMessage) {
	links.mu.Lock()
	defer links.mu.Unlock()
	if links.current != nil {
		links.current.send(message)
	}
}

func (links *pcLinks) release(id string) {
	links.mu.Lock()
	defer links.mu.Unlock()
	if links.current != nil && links.current.id == id {
		links.end(links.current)
	}
}

// EndPCLink ends the desktop app's link, as its close does, and refuses new ones, so that neither its poll nor the
// requests that wait for its replies hold the gateway's shutdown.
func (handler *Handler) EndPCLink() {
	links := &handler.app.pcLinks
	links.mu.Lock()
	defer links.mu.Unlock()
	links.stopped = true
	if links.current != nil {
		links.end(links.current)
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

// readPCLinkRequest reads the body of a request of the desktop app's link. Pages send an Origin or Sec-Fetch-* header
// with every POST, and can't send the link's header across origins without CORS, so no page can play the link. Plain
// HTTP from another machine would let anyone on the way slip a request into a poll.
func (app *application) readPCLinkRequest(response http.ResponseWriter, request *http.Request) (pcLinkBody, bool) {
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
	if refusal != "" {
		writeJSONStatus(response, http.StatusForbidden, map[string]string{"error": refusal})
		return body, false
	}
	request.Body = http.MaxBytesReader(response, request.Body, 1<<20)
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.Link == "" {
		writeJSONStatus(response, http.StatusBadRequest, map[string]string{"error": "send a JSON object that names the link"})
		return body, false
	}
	return body, true
}

func (app *application) pollPCLink(response http.ResponseWriter, request *http.Request) {
	body, ok := app.readPCLinkRequest(response, request)
	if !ok {
		return
	}
	messages, err := app.pcLinks.poll(request.Context(), body.Link, body.Open)
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
	body, ok := app.readPCLinkRequest(response, request)
	if !ok {
		return
	}
	if body.ID == "" || body.Error == "" && len(body.Message) == 0 || len(body.Message) > pcMessageLimit {
		writeJSONStatus(response, http.StatusBadRequest, map[string]string{"error": "a reply needs the request's id, and a message of up to 256 KiB or an error"})
		return
	}
	switch status := app.pcLinks.reply(body); status {
	case http.StatusConflict:
		writeJSONStatus(response, status, map[string]string{"error": "the link is not the current one"})
	case http.StatusNotFound:
		writeJSONStatus(response, status, map[string]string{"error": "the request no longer waits for a reply"})
	default:
		writeJSON(response, struct{}{})
	}
}

func (app *application) closePCLink(response http.ResponseWriter, request *http.Request) {
	body, ok := app.readPCLinkRequest(response, request)
	if !ok {
		return
	}
	app.pcLinks.release(body.Link)
	writeJSON(response, struct{}{})
}

// pcSSHAgent relays a message of an SSH agent connection of gripi pc ssh to the computer of the desktop app's link,
// and returns its reply, or reports that the connection closed.
func (app *application) pcSSHAgent(response http.ResponseWriter, request *http.Request) {
	if refusal := app.pcAccessRefusal(); refusal != "" {
		writeJSONStatus(response, http.StatusForbidden, map[string]string{"error": refusal})
		return
	}
	if !parseForm(response, request) {
		return
	}
	session, connection, closing := request.PostFormValue("session"), request.PostFormValue("connection"), request.PostFormValue("close") == "1"
	message, err := base64.StdEncoding.DecodeString(request.PostFormValue("message"))
	if connection == "" || !closing && (err != nil || len(message) == 0 || len(message) > pcMessageLimit) {
		writeJSONStatus(response, http.StatusBadRequest, map[string]string{"error": "an agent request needs a connection, and a message of up to 256 KiB in base64 or close=1"})
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
		app.pcLinks.notify(pcMessage{Type: "ssh_close", Session: session, Connection: connection})
		writeJSON(response, struct{}{})
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
	reply, err := app.pcLinks.ask(request.Context(), pcMessage{Type: "ssh", ID: id, Session: session, SessionName: name, Connection: connection, Message: message})
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
