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

	"github.com/melounvitek/gripi/internal/agent"
	"github.com/melounvitek/gripi/internal/sessions"
)

const (
	completionNotificationQueueSize   = 64
	completionNotificationGracePeriod = time.Minute
	maxNotificationURLBytes           = 512
)

type completedReply struct {
	client         agent.Client
	path           string
	text           string
	id             string
	observedAt     time.Time
	readCount      int
	readCountKnown bool
}

type clientCompletionState struct {
	pending *completedReply
	// Preserve read suppression between the reply's end and the run's settling.
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
	clients     map[agent.Client]*clientCompletionState
}

func newCompletionNotifier(app *application) *completionNotifier {
	ctx, cancel := context.WithCancel(context.Background())
	return &completionNotifier{
		app: app, ctx: ctx, cancel: cancel,
		queue: make(chan completedReply, completionNotificationQueueSize), done: make(chan struct{}),
		now: time.Now, waitUntil: waitUntilNotificationDeadline, gracePeriod: completionNotificationGracePeriod,
		clients: make(map[agent.Client]*clientCompletionState),
	}
}

func (notifier *completionNotifier) Observe(client agent.Client, lifecycle agent.Lifecycle) {
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
	reply := lifecycle.Reply
	switch lifecycle.Step {
	case agent.RunStarted:
		state.pending, state.message = nil, nil
		state.responseCount = notifier.responseCount(path, state.responseCount)
		if err := notifier.app.gatewayState.BeginCompletion(path, state.responseCount); err != nil {
			log.Printf("begin session completion: %v", err)
		}
	case agent.ReplyEnded:
		state.message = nil
		if reply.Text != "" {
			state.responseCount++
		}
		if reply.Final {
			state.message = &completedReply{client: client, path: path, text: reply.Text, id: reply.ID}
			notifier.captureReadCount(state.message)
		}
	case agent.RunEnded:
		state.pending = nil
		if reply.Final {
			pending := &completedReply{client: client, path: path, text: reply.Text, id: reply.ID}
			if state.message != nil && state.message.id == pending.id {
				pending.path = state.message.path
				pending.readCount, pending.readCountKnown = state.message.readCount, state.message.readCountKnown
			} else {
				notifier.captureReadCount(pending)
			}
			state.pending = pending
		}
	case agent.RunSettled:
		ready = state.pending
		delete(notifier.clients, client)
		if notifier.app.guard != nil {
			if sync := notifier.app.guard.KnownBlocked(path); sync != nil && sync.Mode == agent.SyncExternalFollow {
				ready = nil
			}
		}
		if ready != nil {
			published, err := notifier.app.gatewayState.Complete(path, sessions.Completion{ID: ready.id, Preview: agent.NotificationPreview(ready.text), ResponseCount: notifier.responseCount(path, state.responseCount)})
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
	if session, found := notifier.app.sessionStore.Session(path); found {
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
	if notifier.app.guard != nil {
		if state := notifier.app.guard.KnownBlocked(reply.path); state != nil && state.Mode == agent.SyncExternalFollow {
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
	if notifier.app.guard != nil {
		for {
			if state := notifier.app.guard.KnownBlocked(path); state != nil && state.Mode == agent.SyncExternalFollow {
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			// Inspect only in the delivery worker: RPC callbacks must not wait for RPC responses.
			state := notifier.app.guard.InspectIfAvailable(ctx, path, false)
			if state != nil {
				if state.Mode == agent.SyncExternalFollow {
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
	if session, found := notifier.app.sessionStore.Session(path); found && strings.TrimSpace(session.DisplayName) != "" {
		title = agent.NotificationPreview(session.DisplayName)
	}
	payload, err := json.Marshal(map[string]string{
		"type":  "gripi-notification",
		"title": title,
		"body":  agent.NotificationPreview(reply.text),
		"tag":   completedReplyTag(path, reply.id),
		"url":   completedReplyURL(path, notifier.app.agentOf(path)),
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

	state, err := reply.client.State(ctx)
	if err != nil {
		return path, nil
	}
	reported := state.Key
	if configured, valid := notifier.app.sessionStore.Canonical(reported); valid {
		reported = configured
	} else {
		return path, nil
	}
	session, found := notifier.app.sessionStore.Session(reported)
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

// completedReplyURL opens the session that replied. A Hermes conversation shows only in the Hermes panel.
func completedReplyURL(path, agentName string) string {
	if agentName == "hermes" {
		return "/hermes"
	}
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
