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
	completionNotificationQueueSize = 64
	maxNotificationURLBytes         = 512
)

type completedReply struct {
	client rpc.RPCClient
	path   string
	text   string
	id     string
}

type completionNotifier struct {
	app     *application
	ctx     context.Context
	cancel  context.CancelFunc
	queue   chan completedReply
	done    chan struct{}
	mu      sync.Mutex
	started bool
	closed  bool
}

func newCompletionNotifier(app *application) *completionNotifier {
	ctx, cancel := context.WithCancel(context.Background())
	return &completionNotifier{
		app: app, ctx: ctx, cancel: cancel,
		queue: make(chan completedReply, completionNotificationQueueSize), done: make(chan struct{}),
	}
}

func (notifier *completionNotifier) Observe(client *rpc.Client, event map[string]any) {
	text, completed := completedAssistantReply(event)
	if !completed {
		return
	}
	path := notifier.app.rpcClients.PathForClient(client)
	if path == "" {
		log.Print("drop completed-reply notification: session is no longer registered")
		return
	}
	reply := completedReply{client: client, path: path, text: text, id: completedReplyID(event)}

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
			ctx, cancel := context.WithTimeout(notifier.ctx, 50*time.Second)
			if err := notifier.deliver(ctx, reply); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("deliver completed-reply notification: %v", err)
			}
			cancel()
		}
	}
}

func (notifier *completionNotifier) deliver(ctx context.Context, reply completedReply) error {
	path, err := notifier.sessionPath(ctx, reply)
	if err != nil {
		return err
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
		if !notifier.app.notificationPresence.Focused(presenceOwner, path) {
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
	if !notifier.app.config.MultiUserMode {
		return reported, nil
	}
	owner, err := notifier.app.ownershipStore.Owner(path)
	if err != nil || owner == "" {
		return path, err
	}
	if _, err := notifier.app.ownershipStore.Claim(reported, owner); err != nil {
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
