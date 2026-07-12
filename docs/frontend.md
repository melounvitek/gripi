# Frontend architecture

The gateway uses server-rendered ERB with native JavaScript modules and no frontend build step. Sinatra serves the files in `public/assets/` with revalidation enabled. `views/index.erb` provides the page shell and server-owned data attributes.

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

- `views/_message_article.erb` renders persisted history.
- `LiveMessageParser` and `LiveMessageRenderer` render newly received events.

Changes to message presentation or supported Pi event shapes must check both paths. Shared CSS classes and semantic parity are covered by regression tests, while intentional live-only state includes streaming, optimistic, and temporary tool progress markers.

## Testing

Ruby tests exercise HTTP assets, HTML contracts, JavaScript modules through Node, controller lifecycle behavior, and representative SSR/live semantics. Run:

```sh
mise run test
npm run desktop:check
for file in public/assets/*.js; do node --check "$file"; done
```
