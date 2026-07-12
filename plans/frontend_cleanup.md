# Frontend cleanup

## Goal

Extract the inline frontend assets from `views/index.erb`, establish explicit page/session lifecycles, and split the JavaScript into feature-oriented native ES modules without changing user-visible behavior.

## Guardrails

- No frontend framework or production bundler.
- Preserve full-page fallbacks and native Pi behavior.
- Keep server-rendered history and live event rendering aligned without combining their distinct workflows.
- Split code only at clear ownership boundaries.
- Keep asset caching explicit so gateway updates cannot retain stale code.

## Completed TDD rounds

1. Added tested static asset serving and moved server values into HTML data contracts.
2. Extracted CSS verbatim and redirected CSS assertions to the asset.
3. Extracted JavaScript verbatim and redirected JavaScript assertions to the asset.
4. Converted the entrypoint to a native module, separated page/session binding, and prevented duplicate global listeners.
5. Extracted low-coupling constants, model, formatting, URL, and shortcut helpers with direct tests.
6. Added controllers for gateway updates, access requests, project selection, new-session forms, sidebar, conversation viewport/history, and current-session find.
7. Separated pure Pi event parsing, live message DOM rendering, and cancellable server Markdown rendering from app-level polling and policy.
8. Documented the frontend lifecycle and completed automated and Chromium verification.

## Deliberate boundaries

`app.js` continues to coordinate session switching, polling, composer behavior, status, notifications, modal policy, and global shortcuts. Composer and generic modal extraction were audited and deferred because they currently coordinate several features; extracting them would require a broad callback bag rather than establish clearer ownership.

Server-rendered history remains in `_message_article.erb`, while live messages use `LiveMessageParser` and `LiveMessageRenderer`. Regression tests cover representative semantic parity. Full normalized DOM-tree parity fixtures are tracked in `TODO.md` rather than adding a production frontend dependency solely for tests.

## Validation

- `mise run test`
- `npm run desktop:check`
- Syntax checks for every `public/assets/*.js` module
- `git diff --check`
- Chromium desktop and mobile rendering
- Chromium in-app session switching, history restoration, module loading, modal enhancement, and runtime exception checks
- Independent simplification/correctness review and rereview
