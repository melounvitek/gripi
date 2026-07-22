# Frontend architecture

The Go gateway renders HTML templates from `internal/server/templates/` and serves native JavaScript modules from `public/assets/` with no frontend build step. `internal/server/templates/index.html` provides the page shell and server-owned data attributes.

## Lifecycles

Page-lifetime listeners are registered once by `public/assets/app.js`. Replaceable session elements are rebound after in-app session navigation.

`app.js` coordinates session navigation, event polling, composer behavior, modal policy, status, and notifications. Feature modules own narrower lifecycles:

- `SidebarController` owns sidebar refreshes and replacement.
- `ConversationController` owns viewport state, history loading, and scrolling.
- `CurrentSessionFindController` owns conversation find state and highlights.
- `LiveMessageParser` interprets Pi event and message shapes without touching the DOM.
- `LiveMessageRenderer` owns live message DOM state.
- `ServerMarkdownRenderer` owns cancellable server Markdown requests.
- The access, gateway update, project select, and new-session form controllers own their respective timers and listeners.

Before replacing a session view, `app.js` resets controllers that hold session DOM or asynchronous work. After replacement, it binds the new elements before polling resumes.

## Rendering

Conversation messages have two rendering paths:

- `internal/server/templates/message.html` renders persisted history.
- `LiveMessageParser` and `LiveMessageRenderer` render newly received events.

Changes to message presentation or supported Pi event shapes must check both paths. Shared CSS classes and server-rendered semantics are covered by Go rendering tests; native Node tests exercise representative live shapes and persisted-message deduplication; the browser contract checks the integrated paths. Intentional live-only state includes streaming, optimistic, and temporary tool progress markers.

## Testing

Run the directly importable browser modules, demo, and JavaScript syntax checks with:

```sh
mise run frontend-check
mise run pi-extension-check
```

The native suites cover Markdown cancellation, representative SSR/live shapes and deduplication, terminal update races, sidebar and history concurrency, first-touch project selection, and tree behavior. Managed Playwright covers out-of-order session orchestration and extension UI timeout/retry/queue behavior in the production app. The native suites do not replace integrated DOM coverage. The Pi extension check uses the installed Pi package to exercise the native tree bridge. Use `mise run test-go` for server-rendered HTML and route contracts, and `mise run e2e` for complete browser lifecycles. See [testing](testing.md) for the full matrix.
