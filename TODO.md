# Follow-up work

- [ ] Cover a concurrent prompt between tree-navigation abort, client closure, and navigation. These steps currently use separate synchronization scopes in `internal/server/action_routes.go`. Verify the gap with a deterministic regression before changing the client-restart workflow.
