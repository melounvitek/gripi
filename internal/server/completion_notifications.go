package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/melounvitek/gripi/internal/rpc"
	"github.com/melounvitek/gripi/internal/sessions"
)

const (
	completionNotificationQueueSize   = 64
	completionNotificationGracePeriod = time.Minute
	maxNotificationURLBytes           = 512
)

type completedReply struct {
	client         rpc.RPCClient
	path           string
	text           string
	id             string
	observedAt     time.Time
	readCount      int
	readCountKnown bool
}

type clientCompletionState struct {
	pending *completedReply
	// Preserve read suppression between message_end and agent_settled.
	message *completedReply
	// Synthetic pending paths have no session file to count yet.
	responseCount int
}

type completionNotifier struct {
	app         *application
	ctx         context.Context
	cancel      context.CancelFunc
	queue       chan completedReply
	done        chan struct{}
	now         func() time.Time
	waitUntil   func(context.Context, time.Time) bool
	gracePeriod time.Duration
	mu          sync.Mutex
	started     bool
	closed      bool
	clients     map[*rpc.Client]*clientCompletionState
}

func newCompletionNotifier(app *application) *completionNotifier {
	ctx, cancel := context.WithCancel(context.Background())
	return &completionNotifier{
		app: app, ctx: ctx, cancel: cancel,
		queue: make(chan completedReply, completionNotificationQueueSize), done: make(chan struct{}),
		now: time.Now, waitUntil: waitUntilNotificationDeadline, gracePeriod: completionNotificationGracePeriod,
		clients: make(map[*rpc.Client]*clientCompletionState),
	}
}

func (notifier *completionNotifier) Observe(client *rpc.Client, event map[string]any) {
	switch event["type"] {
	case "agent_start", "message_end", "agent_end", "agent_settled":
	default:
		return
	}
	notifier.mu.Lock()
	notifier.pruneClientsLocked()
	path := notifier.app.rpcClients.PathForClient(client)
	if notifier.closed || path == "" {
		notifier.mu.Unlock()
		return
	}
	state := notifier.clients[client]
	if state == nil {
		state = &clientCompletionState{}
		notifier.clients[client] = state
	}
	var ready *completedReply
	switch event["type"] {
	case "agent_start":
		state.pending, state.message = nil, nil
		state.responseCount = notifier.responseCount(path, state.responseCount)
		if err := notifier.app.gatewayState.BeginCompletion(path, state.responseCount); err != nil {
			log.Printf("begin session completion: %v", err)
		}
	case "message_end":
		message, _ := event["message"].(map[string]any)
		if message["role"] != "assistant" {
			break
		}
		state.message = nil
		if strings.TrimSpace(sessions.FinalAssistantText(message["content"])) != "" {
			state.responseCount++
		}
		if text, eligible := completedAssistantReply(event); eligible {
			state.message = &completedReply{client: client, path: path, text: text, id: completedReplyID(event)}
			notifier.captureReadCount(state.message)
		}
	case "agent_end":
		state.pending = nil
		messages, _ := event["messages"].([]any)
		for index := len(messages) - 1; index >= 0; index-- {
			message, _ := messages[index].(map[string]any)
			if message["role"] != "assistant" {
				continue
			}
			candidate := map[string]any{"type": "message_end", "message": message}
			if text, eligible := completedAssistantReply(candidate); eligible {
				reply := &completedReply{client: client, path: path, text: text, id: completedReplyID(candidate)}
				if state.message != nil && state.message.id == reply.id {
					reply.path = state.message.path
					reply.readCount, reply.readCountKnown = state.message.readCount, state.message.readCountKnown
				} else {
					notifier.captureReadCount(reply)
				}
				state.pending = reply
			}
			// Never fall back to an earlier eligible assistant reply.
			break
		}
	case "agent_settled":
		ready = state.pending
		delete(notifier.clients, client)
		if notifier.app.synchronizer != nil {
			if sync := notifier.app.synchronizer.KnownBlocked(path); sync != nil && sync.Mode == sessions.SyncExternalFollow {
				ready = nil
			}
		}
		if ready != nil {
			published, err := notifier.app.gatewayState.Complete(path, sessions.Completion{ID: ready.id, Preview: sessions.NotificationPreview(ready.text), ResponseCount: notifier.responseCount(path, state.responseCount)})
			if err != nil {
				log.Printf("save session completion: %v", err)
			}
			if !published {
				ready = nil
			}
		}
	}
	notifier.mu.Unlock()
	if ready != nil {
		notifier.schedule(*ready)
	}
}

// Only in-flight candidates retain clients; published metadata lives in GatewayState.
func (notifier *completionNotifier) pruneClientsLocked() {
	for client := range notifier.clients {
		if notifier.app.rpcClients.PathForClient(client) == "" {
			delete(notifier.clients, client)
		}
	}
}

func (notifier *completionNotifier) responseCount(path string, fallback int) int {
	store := sessions.Store{Root: notifier.app.config.SessionsRoot, Home: notifier.app.config.Home, Cache: notifier.app.sessionCache}
	if session, found := store.Session(path); found {
		return session.AssistantResponseCount
	}
	return fallback
}

func (notifier *completionNotifier) captureReadCount(reply *completedReply) {
	if notifier.app.gatewayState == nil || reply.readCountKnown {
		return
	}
	readCount, err := notifier.app.gatewayState.ReadCount(reply.path)
	if err != nil {
		log.Printf("capture completed-reply read state: %v", err)
	} else {
		reply.readCount = readCount
		reply.readCountKnown = true
	}
}

func (notifier *completionNotifier) schedule(reply completedReply) {
	if notifier.app.synchronizer != nil {
		if state := notifier.app.synchronizer.KnownBlocked(reply.path); state != nil && state.Mode == sessions.SyncExternalFollow {
			return
		}
	}
	reply.observedAt = notifier.now()
	notifier.captureReadCount(&reply)
	if _, _, pending := notifier.app.pendingSessions.Current(reply.path); !pending {
		reply.client = nil
	}

	notifier.mu.Lock()
	if notifier.closed {
		notifier.mu.Unlock()
		return
	}
	if !notifier.started {
		notifier.started = true
		go notifier.run()
	}
	notifier.mu.Unlock()

	select {
	case notifier.queue <- reply:
	default:
		log.Print("drop completed-reply notification: delivery queue is full")
	}
}

func (notifier *completionNotifier) Close(ctx context.Context) error {
	notifier.mu.Lock()
	if notifier.closed {
		notifier.mu.Unlock()
		return nil
	}
	notifier.closed = true
	clear(notifier.clients)
	started := notifier.started
	notifier.cancel()
	notifier.mu.Unlock()
	if !started {
		return nil
	}
	select {
	case <-notifier.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (notifier *completionNotifier) run() {
	defer close(notifier.done)
	for {
		select {
		case <-notifier.ctx.Done():
			return
		case reply := <-notifier.queue:
			if !notifier.waitUntil(notifier.ctx, reply.observedAt.Add(notifier.gracePeriod)) {
				return
			}

			ctx, cancel := context.WithTimeout(notifier.ctx, 50*time.Second)
			if err := notifier.deliver(ctx, reply); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("deliver completed-reply notification: %v", err)
			}
			cancel()
		}
	}
}

func waitUntilNotificationDeadline(ctx context.Context, deadline time.Time) bool {
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (notifier *completionNotifier) deliver(ctx context.Context, reply completedReply) error {
	path, err := notifier.sessionPath(ctx, reply)
	if err != nil {
		return err
	}
	if notifier.app.synchronizer != nil {
		for {
			if state := notifier.app.synchronizer.KnownBlocked(path); state != nil && state.Mode == sessions.SyncExternalFollow {
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			// Inspect only in the delivery worker: RPC callbacks must not wait for RPC responses.
			state := notifier.app.synchronizer.InspectIfAvailable(ctx, path, false)
			if state != nil {
				if state.Mode == sessions.SyncExternalFollow {
					return nil
				}
				break
			}
			if !waitUntilNotificationDeadline(ctx, time.Now().Add(50*time.Millisecond)) {
				return ctx.Err()
			}
		}
	}
	if reply.readCountKnown && notifier.app.gatewayState != nil {
		readBaselines := map[string]int{reply.path: reply.readCount}
		if path != reply.path {
			readBaselines[path] = 0
		}
		for readPath, baseline := range readBaselines {
			readCount, err := notifier.app.gatewayState.ReadCount(readPath)
			if err != nil {
				log.Printf("check completed-reply read state: %v", err)
				continue
			}
			if readCount > baseline {
				return nil
			}
		}
	}

	owners, err := notifier.owners(path)
	if err != nil {
		return err
	}
	if len(owners) == 0 {
		return nil
	}
	deliveryOwners := make([]string, 0, len(owners))
	for _, owner := range owners {
		presenceOwner := singleUserOwner
		if notifier.app.config.MultiUserMode {
			presenceOwner = owner
		}
		focused := notifier.app.notificationPresence.Focused(presenceOwner, path)
		if !focused && reply.path != path {
			focused = notifier.app.notificationPresence.Focused(presenceOwner, reply.path)
		}
		if !focused {
			deliveryOwners = append(deliveryOwners, owner)
		}
	}
	if len(deliveryOwners) == 0 {
		return nil
	}

	title := "current session"
	store := sessions.Store{Root: notifier.app.config.SessionsRoot, Home: notifier.app.config.Home, Cache: notifier.app.sessionCache}
	if session, found := store.Session(path); found && strings.TrimSpace(session.DisplayName) != "" {
		title = sessions.NotificationPreview(session.DisplayName)
	}
	payload, err := json.Marshal(map[string]string{
		"type":  "gripi-notification",
		"title": title,
		"body":  sessions.NotificationPreview(reply.text),
		"tag":   completedReplyTag(path, reply.id),
		"url":   completedReplyURL(path),
	})
	if err != nil {
		return err
	}

	var failures []error
	var failuresMu sync.Mutex
	var deliveries sync.WaitGroup
	for _, owner := range deliveryOwners {
		deliveries.Add(1)
		go func(owner string) {
			defer deliveries.Done()
			if err := notifier.app.pushNotifier.Deliver(ctx, owner, payload); err != nil {
				failuresMu.Lock()
				failures = append(failures, err)
				failuresMu.Unlock()
			}
		}(owner)
	}
	deliveries.Wait()
	return errors.Join(failures...)
}

func (notifier *completionNotifier) sessionPath(ctx context.Context, reply completedReply) (string, error) {
	path, pendingCWD, pending := notifier.app.pendingSessions.Current(reply.path)
	if !pending {
		return path, nil
	}

	state, err := reply.client.GetState(ctx)
	if err != nil {
		return path, nil
	}
	reported := sessionFileFrom(state)
	if configured, valid := sessions.ConfiguredSessionPath(notifier.app.config.SessionsRoot, reported); valid {
		reported = configured
	} else {
		return path, nil
	}
	store := sessions.Store{Root: notifier.app.config.SessionsRoot, Home: notifier.app.config.Home, Cache: notifier.app.sessionCache}
	session, found := store.Session(reported)
	if !found || session.CWD != pendingCWD {
		return path, nil
	}
	reported = session.Path
	err = notifier.app.remapPendingRPCClient(path, reported, func() (func() error, error) {
		if !notifier.app.config.MultiUserMode {
			return nil, nil
		}
		owner, err := notifier.app.ownershipStore.Owner(path)
		if err != nil {
			return nil, err
		}
		if owner == "" {
			return nil, errors.New("pending session has no owner")
		}
		claimed, err := notifier.app.ownershipStore.Claim(reported, owner)
		if err != nil || !claimed {
			return nil, err
		}
		return func() error { return notifier.app.ownershipStore.Release(reported, owner) }, nil
	})
	if err != nil {
		return path, err
	}
	return reported, nil
}

func (notifier *completionNotifier) owners(path string) ([]string, error) {
	if notifier.app.config.MultiUserMode {
		workspaceID, err := notifier.app.ownershipStore.Owner(path)
		if err != nil || workspaceID == "" {
			return nil, err
		}
		approved, err := notifier.app.workspaceStore.Approved(workspaceID)
		if err != nil {
			return nil, err
		}
		owner := "workspace:" + workspaceID
		if !approved {
			return nil, notifier.app.pushSubscriptions.RemoveOwner(owner)
		}
		return []string{owner}, nil
	}
	if !notifier.app.browserAccessEnabled() {
		return []string{singleUserOwner}, nil
	}

	approved, err := notifier.app.browserStore.ApprovedTokenDigests()
	if err != nil {
		return nil, err
	}
	owners, err := notifier.app.pushSubscriptions.Owners()
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(owners))
	for _, owner := range owners {
		digest, browserOwner := strings.CutPrefix(owner, "browser:")
		if browserOwner && approved[digest] {
			result = append(result, owner)
			continue
		}
		if err := notifier.app.pushSubscriptions.RemoveOwner(owner); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func completedReplyURL(path string) string {
	result := "/?session=" + url.QueryEscape(path)
	if len(result) > maxNotificationURLBytes {
		return "/"
	}
	return result
}

func completedReplyTag(path, replyID string) string {
	digest := sha256.Sum256([]byte(path + "\x00" + replyID))
	return "gripi-final-reply:" + hex.EncodeToString(digest[:16])
}

func completedReplyID(event map[string]any) string {
	message, _ := event["message"].(map[string]any)
	for _, value := range []any{message["id"], message["messageId"], event["id"], event["messageId"]} {
		if encoded, err := json.Marshal(value); err == nil && string(encoded) != "null" && string(encoded) != `""` {
			digest := sha256.Sum256(encoded)
			return hex.EncodeToString(digest[:16])
		}
	}
	stable := any(message)
	if len(message) == 0 {
		stable = event
	}
	encoded, _ := json.Marshal(stable)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:16])
}

func completedAssistantReply(event map[string]any) (string, bool) {
	if event["type"] != "message_end" {
		return "", false
	}
	message := event
	if nested, ok := event["message"].(map[string]any); ok {
		message = nested
	}
	if role, _ := message["role"].(string); role != "" && role != "assistant" {
		return "", false
	}
	if stopReason, _ := message["stopReason"].(string); stopReason != "" && stopReason != "stop" && stopReason != "length" {
		return "", false
	}
	text := sessions.FinalAssistantText(message["content"])
	return text, text != ""
}
