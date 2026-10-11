package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
)

// Router serves the sessions of several agents as one adapter. A session belongs to the agent whose store accepts its
// key, and a new session starts with the agent that StartOptions.Agent names, or the first one.
type Router struct {
	agents []RoutedAgent
	guards []Guard
}

type RoutedAgent struct {
	Name    string
	Adapter Adapter
}

var _ Adapter = (*Router)(nil)

func NewRouter(agents ...RoutedAgent) *Router {
	return &Router{agents: agents}
}

var errNoAgent = errors.New("no agent serves this session")

// owner returns the index of the agent whose keys look like key, or -1.
func (router *Router) owner(key string) int {
	for index, routed := range router.agents {
		if routed.Adapter.Store().ValidKey(key) {
			return index
		}
	}
	return -1
}

func (router *Router) Start(key string, options StartOptions) (Client, error) {
	index := router.owner(key)
	if index < 0 {
		return nil, errNoAgent
	}
	return router.agents[index].Adapter.Start(key, options)
}

func (router *Router) StartInCWD(cwd, forkFrom string, options StartOptions) (Client, error) {
	if options.Agent == "" {
		return router.agents[0].Adapter.StartInCWD(cwd, forkFrom, options)
	}
	for _, routed := range router.agents {
		if routed.Name == options.Agent {
			return routed.Adapter.StartInCWD(cwd, forkFrom, options)
		}
	}
	return nil, fmt.Errorf("unknown agent %q", options.Agent)
}

func (router *Router) DisplaySettings(cwd string) DisplaySettings {
	return router.agents[0].Adapter.DisplaySettings(cwd)
}

func (router *Router) Composer() Composer {
	return router.agents[0].Adapter.Composer()
}

func (router *Router) Store() Store {
	return routerStore{router}
}

func (router *Router) Guard(clients *Registry) (Guard, error) {
	router.guards = make([]Guard, len(router.agents))
	for index, routed := range router.agents {
		guard, err := routed.Adapter.Guard(clients)
		if err != nil {
			return nil, err
		}
		router.guards[index] = guard
	}
	return routerGuard{router}, nil
}

func (router *Router) Close() error {
	var result error
	for _, routed := range router.agents {
		result = errors.Join(result, routed.Adapter.Close())
	}
	return result
}

type routerStore struct{ router *Router }

func (store routerStore) of(key string) (Store, bool) {
	index := store.router.owner(key)
	if index < 0 {
		return nil, false
	}
	return store.router.agents[index].Adapter.Store(), true
}

func (store routerStore) Sessions() ([]*Session, error) {
	sessions, _, err := store.SessionsDeferringMetadata(func(string) bool { return false })
	return sessions, err
}

func (store routerStore) SessionsDeferringMetadata(deferFor func(key string) bool) ([]*Session, bool, error) {
	var all []*Session
	deferred := false
	for _, routed := range store.router.agents {
		sessions, deferredHere, err := routed.Adapter.Store().SessionsDeferringMetadata(deferFor)
		if err != nil {
			return nil, false, err
		}
		all = append(all, sessions...)
		deferred = deferred || deferredHere
	}
	sort.SliceStable(all, func(left, right int) bool {
		return all[left].ConversationActivityAt.After(all[right].ConversationActivityAt)
	})
	return all, deferred, nil
}

func (store routerStore) Session(key string) (*Session, bool) {
	if owner, ok := store.of(key); ok {
		return owner.Session(key)
	}
	return nil, false
}

func (store routerStore) Window(key, leafID string, leafSupplied bool, cursor, after *int) (Window, error) {
	if owner, ok := store.of(key); ok {
		return owner.Window(key, leafID, leafSupplied, cursor, after)
	}
	return Window{}, fs.ErrNotExist
}

func (store routerStore) Status(key string) (Status, error) {
	if owner, ok := store.of(key); ok {
		return owner.Status(key)
	}
	return Status{}, fs.ErrNotExist
}

func (store routerStore) Generation(key string) string {
	if owner, ok := store.of(key); ok {
		return owner.Generation(key)
	}
	return ""
}

func (store routerStore) Image(key, entryID string, index int) ([]byte, string, error) {
	if owner, ok := store.of(key); ok {
		return owner.Image(key, entryID, index)
	}
	return nil, "", fs.ErrNotExist
}

func (store routerStore) SubagentToolCallContext(key, leafID string, ids []string) map[string]ToolCallContext {
	if owner, ok := store.of(key); ok {
		return owner.SubagentToolCallContext(key, leafID, ids)
	}
	return nil
}

func (store routerStore) Exists(key string) bool {
	owner, ok := store.of(key)
	return ok && owner.Exists(key)
}

func (store routerStore) Canonical(key string) (string, bool) {
	if owner, ok := store.of(key); ok {
		return owner.Canonical(key)
	}
	return "", false
}

func (store routerStore) Aliases(key string) []string {
	if owner, ok := store.of(key); ok {
		return owner.Aliases(key)
	}
	return nil
}

func (store routerStore) ValidKey(key string) bool {
	_, ok := store.of(key)
	return ok
}

// PendingKey serves the first agent, the only one whose new sessions need a key before the agent names them.
func (store routerStore) PendingKey() string {
	return store.router.agents[0].Adapter.Store().PendingKey()
}

func (store routerStore) FallbackCWD(key string) string {
	if owner, ok := store.of(key); ok {
		return owner.FallbackCWD(key)
	}
	return store.router.agents[0].Adapter.Store().FallbackCWD(key)
}

func (store routerStore) ExportName(key string) string {
	if owner, ok := store.of(key); ok {
		return owner.ExportName(key)
	}
	return ""
}

func (store routerStore) Delete(key string) (string, error) {
	if owner, ok := store.of(key); ok {
		return owner.Delete(key)
	}
	return "", fs.ErrNotExist
}

func (store routerStore) SaveCache() error {
	var result error
	for _, routed := range store.router.agents {
		result = errors.Join(result, routed.Adapter.Store().SaveCache())
	}
	return result
}

type routerGuard struct{ router *Router }

// of returns the guard of the session's agent, or the first agent's for a key that no agent serves, which refuses it.
func (guard routerGuard) of(key string) Guard {
	return guard.router.guards[max(guard.router.owner(key), 0)]
}

func (guard routerGuard) InspectIfAvailable(ctx context.Context, key string, includePosition bool) *SyncResult {
	return guard.of(key).InspectIfAvailable(ctx, key, includePosition)
}

func (guard routerGuard) ReconcileIfAvailable(ctx context.Context, key string, includePosition bool, after func(SyncResult)) *SyncResult {
	return guard.of(key).ReconcileIfAvailable(ctx, key, includePosition, after)
}

func (guard routerGuard) KnownBlocked(key string) *SyncResult { return guard.of(key).KnownBlocked(key) }

func (guard routerGuard) Forget(key string) { guard.of(key).Forget(key) }

func (guard routerGuard) TakeOver(ctx context.Context, key string, beforeManaged func() error) (SyncResult, error) {
	return guard.of(key).TakeOver(ctx, key, beforeManaged)
}

// Message explains a blocked result, which only the first agent's guard reports.
func (guard routerGuard) Message(result SyncResult) string {
	return guard.router.guards[0].Message(result)
}

func (guard routerGuard) WithExclusiveOperation(key string, call func() error) error {
	return guard.of(key).WithExclusiveOperation(key, call)
}

func (guard routerGuard) WithMutableClient(ctx context.Context, key string, call func(Client) error) error {
	return guard.of(key).WithMutableClient(ctx, key, call)
}

func (guard routerGuard) WithInterruptClient(ctx context.Context, key string, call func(Client) error) error {
	return guard.of(key).WithInterruptClient(ctx, key, call)
}

func (guard routerGuard) WithBashClient(ctx context.Context, key string, call func(Client) error) error {
	return guard.of(key).WithBashClient(ctx, key, call)
}

func (guard routerGuard) WithClientMove(ctx context.Context, key string, touch bool, call func(Client) (string, error), prepare func(string, string) (func() error, error), commit func(string, string)) (string, error) {
	return guard.of(key).WithClientMove(ctx, key, touch, call, prepare, commit)
}

func (guard routerGuard) RetireManagedClientIfAvailable(key string, retire func() (bool, error)) (bool, error) {
	return guard.of(key).RetireManagedClientIfAvailable(key, retire)
}
