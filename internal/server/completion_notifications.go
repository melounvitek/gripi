package server

import (
	"context"
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

const completionNotificationQueueSize = 64

type completedReply struct {
	client rpc.RPCClient
	text   string
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
	case notifier.queue <- completedReply{client: client, text: text}:
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
	path, err := notifier.sessionPath(ctx, reply.client)
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

	title := "current session"
	store := sessions.Store{Root: notifier.app.config.SessionsRoot, Home: notifier.app.config.Home, Cache: notifier.app.sessionCache}
	if session, found := store.Session(path); found && strings.TrimSpace(session.DisplayName) != "" {
		title = session.DisplayName
	}
	payload, err := json.Marshal(map[string]string{
		"type":  "gripi-notification",
		"title": title,
		"body":  sessions.NotificationPreview(reply.text),
		"tag":   "gripi-final-reply:" + sessions.SessionHash(path),
		"url":   "/?session=" + url.QueryEscape(path),
	})
	if err != nil {
		return err
	}

	var failures []error
	for _, owner := range owners {
		if err := notifier.app.pushNotifier.Deliver(ctx, owner, payload); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (notifier *completionNotifier) sessionPath(ctx context.Context, client rpc.RPCClient) (string, error) {
	path := notifier.app.rpcClients.PathForClient(client)
	if path == "" {
		return "", errors.New("completed reply has no registered session")
	}
	if _, pending := notifier.app.pendingSessions.CWD(path); !pending {
		return path, nil
	}

	state, err := client.GetState(ctx)
	if err != nil {
		return path, nil
	}
	reported := sessionFileFrom(state)
	if configured, valid := sessions.ConfiguredSessionPath(notifier.app.config.SessionsRoot, reported); valid {
		reported = configured
	} else {
		return path, nil
	}
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
	text := sessions.FinalAssistantText(message["content"])
	return text, text != ""
}
