# Refactor TODO — Pi Web Gateway production hardening

This file captures the current project audit and turns it into an ordered checklist for future sessions.

## How to use this file

When starting a fresh session, say:

> Pick the next open item from REFACTOR_TODO.md

The agent should:

1. Read this file first.
2. Pick the first unchecked actionable item unless the user names a specific item.
3. Inspect the referenced files before proposing changes.
4. Propose a small implementation plan before editing.
5. Prefer TDD rounds for behavior changes: failing test, minimal fix, focused regression.
6. Keep diffs small and avoid unrelated cleanup.
7. Mark checklist items complete only after implementation and verification.
8. If code changes affect the running web gateway, tell the user a restart is needed; do not restart it unless explicitly asked.

## Overall context

Pi Web Gateway started as a Ruby/Sinatra proof of concept, but it is now a real daily-use app. The current state is healthier than a typical vibe-coded POC: the test suite passes, routes are understandable, browser access exists, and Pi RPC event polling has already received some resilience work.

Current audit snapshot:

- Branch was clean and synced when this file was created.
- Test suite passed: `80 runs, 1068 assertions, 0 failures`.
- Main size hotspots:
  - `app.rb`: routes, helpers, auth, uploads, RPC orchestration, markdown rendering.
  - `views/index.erb`: inline CSS plus all frontend JavaScript.
  - `lib/pi_session_store.rb`: session discovery, JSONL parsing, message grouping, display policy.
- Biggest theme: the app is no longer a POC, but the structure still concentrates too many responsibilities in a few files.

## Priority checklist

### 1. Add RPC request timeouts

Context: `PiRpcClient#request` waits indefinitely until a matching response arrives or the reader exits. Blocking RPC operations can still occupy Puma threads if Pi RPC wedges.

Relevant files:

- `lib/pi_rpc_client.rb`
- `lib/pi_rpc_client_registry.rb`
- `app.rb`
- `test/pi_rpc_client_test.rb`
- `test/app_test.rb`

Checklist:

- [ ] Inspect every route that calls `with_rpc_client` and identify blocking RPC operations.
- [ ] Add a configurable request timeout to `PiRpcClient#request`.
- [ ] Decide whether timeout returns `nil`, raises a gateway-specific error, or returns a structured error response.
- [ ] Handle timeout cleanly in user-facing routes such as `/prompt`, `/commands`, `/compact`, `/abort`, and `/rename`.
- [ ] Add focused tests for successful response, timeout, and reader-exit behavior.
- [ ] Verify the full test suite.
- [ ] Note whether a gateway restart is needed.

### 2. Drain and surface Pi RPC stderr

Context: `PiRpcClient` receives `stderr` from the Pi subprocess but does not read it. If Pi writes enough stderr, the subprocess can block on a full pipe. Recent stderr would also help debugging.

Relevant files:

- `lib/pi_rpc_client.rb`
- `test/pi_rpc_client_test.rb`

Checklist:

- [ ] Add a stderr reader thread when stderr is present.
- [ ] Keep a small ring buffer of recent stderr lines or log them safely.
- [ ] Ensure `close` stops/joins the stderr reader without hanging.
- [ ] Include recent stderr in timeout/error diagnostics where useful.
- [ ] Add tests that stderr is drained and does not affect stdout response handling.
- [ ] Verify focused and full tests.
- [ ] Note whether a gateway restart is needed.

### 3. Harden session path validation

Context: several routes accept a `session` path from request parameters. Approved browser access may be trusted for personal use, but the app should still avoid arbitrary filesystem probing or surprising behavior.

Relevant files:

- `app.rb`
- `lib/pi_session_store.rb`
- `test/app_test.rb`

Checklist:

- [ ] List all routes that accept `params["session"]` or session paths.
- [ ] Define what counts as an allowed known session path.
- [ ] Add a central validation/canonicalization helper.
- [ ] Ensure `/status`, `/commands`, `/prompt`, `/abort`, `/compact`, `/rename`, and `/sessions/new` reject invalid paths consistently.
- [ ] Review pending-session behavior so newly created pending sessions still work.
- [ ] Add request tests for unknown paths, paths outside the sessions root, and valid pending paths.
- [ ] Verify focused and full tests.
- [ ] Note whether a gateway restart is needed.

### 4. Stop deleting sessions during normal page rendering

Context: `prepare_session_view` creates `PiSessionStore` with `delete_missing_cwds: true`, which means a normal `GET /` can delete session files when their recorded cwd no longer exists. That is surprising and risky.

Relevant files:

- `app.rb`
- `lib/pi_session_store.rb`
- `test/app_test.rb`
- `test/pi_session_store_test.rb`

Checklist:

- [ ] Confirm the current deletion behavior and why it was added.
- [ ] Change normal page rendering to not delete session files.
- [ ] If cleanup is still desired, move it to an explicit task/action or clearly named method.
- [ ] Add regression tests that missing-cwd sessions are not deleted by ordinary view preparation.
- [ ] Verify focused and full tests.
- [ ] Note whether a gateway restart is needed.

### 5. Extract CSS from `views/index.erb`

Context: `views/index.erb` is very large because it includes all styles inline. Moving CSS into `public/app.css` will make the template and future style changes easier to review.

Relevant files:

- `views/index.erb`
- `public/app.css` (new)
- `test/app_test.rb` if any response assertions need updating

Checklist:

- [ ] Confirm Sinatra static file serving works for `public/` in this app.
- [ ] Move inline `<style>` contents to `public/app.css`.
- [ ] Add a stylesheet link in `views/index.erb`.
- [ ] Keep visual behavior unchanged.
- [ ] Run existing tests.
- [ ] Manually verify the page loads styles after restart.
- [ ] Note that a gateway restart is needed.

### 6. Extract JavaScript from `views/index.erb`

Context: `views/index.erb` also contains all frontend behavior: polling, live rendering, scrolling, attachments, commands, access overlays, shortcuts, and session switching. This is the largest maintainability hotspot.

Relevant files:

- `views/index.erb`
- `public/app.js` (new)
- `views/_conversation.erb`
- `views/_sidebar.erb`

Checklist:

- [ ] Move inline `<script>` contents to `public/app.js` with minimal behavior changes.
- [ ] Keep server-provided state in data attributes rather than embedding more Ruby in JS.
- [ ] Add a script tag in `views/index.erb`.
- [ ] Verify session switching, event polling, prompt submission, image attachments, slash command list, browser access overlay, and keyboard shortcuts.
- [ ] Run existing tests.
- [ ] Note that a gateway restart is needed.

### 7. Organize frontend JavaScript state

Context: the frontend JS uses many global variables. After extraction to `public/app.js`, grouping state and behavior into a small module/object would reduce accidental coupling.

Relevant files:

- `public/app.js`
- `views/index.erb`
- `views/_conversation.erb`

Checklist:

- [ ] Identify current global variables and the features they belong to.
- [ ] Group state into one app object or a few cohesive modules.
- [ ] Avoid introducing a frontend framework unless explicitly desired.
- [ ] Preserve all existing behavior.
- [ ] Manually verify core UI flows.
- [ ] Run existing tests.
- [ ] Note whether a gateway restart is needed.

### 8. Extract markdown rendering from `app.rb`

Context: `SafeMarkdownRenderer` currently lives in `app.rb`. It is standalone and a good low-risk extraction.

Relevant files:

- `app.rb`
- `lib/safe_markdown_renderer.rb` (new)
- tests covering markdown rendering in `test/app_test.rb`

Checklist:

- [ ] Move `SafeMarkdownRenderer` to `lib/safe_markdown_renderer.rb`.
- [ ] Require it from `app.rb`.
- [ ] Keep sanitization and ordered-list behavior unchanged.
- [ ] Add or keep focused markdown tests.
- [ ] Run existing tests.
- [ ] Note whether a gateway restart is needed.

### 9. Extract session view preparation from `app.rb`

Context: `prepare_session_view` and related helpers handle session grouping, selected-session lookup, read state, attachments, status, pending sessions, and sidebar state. This is a good candidate for a small view model/builder.

Relevant files:

- `app.rb`
- `views/_sidebar.erb`
- `views/_conversation.erb`
- possible new file: `lib/session_view_builder.rb`
- `test/app_test.rb`

Checklist:

- [ ] Identify all instance variables required by the sidebar and conversation templates.
- [ ] Create a small builder/view object that prepares those values.
- [ ] Keep route behavior unchanged.
- [ ] Avoid over-abstracting simple formatting helpers unless they belong with the builder.
- [ ] Add or update request tests around selected session, unread state, pending sessions, and sidebar groups.
- [ ] Run existing tests.
- [ ] Note whether a gateway restart is needed.

### 10. Separate `PiSessionStore` parsing from presentation policy

Context: `PiSessionStore` discovers session files, parses JSONL, derives metadata, groups assistant content, pairs tool calls/results, and builds display-oriented summaries. Future Pi protocol changes will be easier to handle if parsing and presentation are separated.

Relevant files:

- `lib/pi_session_store.rb`
- possible new files:
  - `lib/pi_session_parser.rb`
  - `lib/pi_message_presenter.rb`
- `test/pi_session_store_test.rb`

Checklist:

- [ ] Characterize current parsing behavior with tests before moving code.
- [ ] Extract JSONL entry/message parsing without changing public behavior.
- [ ] Extract display-oriented tool summary/message grouping logic if still useful.
- [ ] Keep `PiSessionStore` responsible mainly for discovery, caching, and public access methods.
- [ ] Verify all existing session rendering tests.
- [ ] Run the full test suite.
- [ ] Note whether a gateway restart is needed.

### 11. Improve file-backed state safety

Context: stores use process-local mutexes and atomic-ish temp-file renames. This is fine for one Puma process, but fragile with multiple processes or concurrent writers.

Relevant files:

- `lib/browser_access_store.rb`
- `lib/gateway_read_state_store.rb`
- `lib/pi_attachment_store.rb`

Checklist:

- [ ] Review read/modify/write flows in each file-backed store.
- [ ] Use unique temp paths consistently for JSON state writes.
- [ ] Consider file locks for cross-process read/modify/write safety.
- [ ] Add tests for repeated updates and temp-file behavior where practical.
- [ ] Document whether the app assumes single-process Puma.
- [ ] Run existing tests.
- [ ] Note whether a gateway restart is needed.

### 12. Add project README / operational docs

Context: there is no `README.md`. A real app should document setup, environment variables, run commands, and operational caveats.

Relevant files:

- `README.md` (new)
- `Gemfile`
- `config.ru`
- `AGENTS.md`

Checklist:

- [ ] Document what the app does.
- [ ] Document required env vars, especially `PI_GATEWAY_ADMIN_PASSWORD` and `PI_GATEWAY_ENV_PATH`.
- [ ] Document optional paths: sessions root, attachments root, read-state path, browser-access path, RPC idle timeout.
- [ ] Document local run command.
- [ ] Document that the deployed dev server needs a restart to pick up code/frontend changes.
- [ ] Document current assumptions such as single-process file-backed state, if still true.

### 13. Add lightweight integration/smoke checks

Context: tests are solid for Ruby behavior, but there is little coverage for browser behavior and real Pi RPC process integration.

Relevant files:

- `test/`
- possible new smoke script under `bin/` or `script/`

Checklist:

- [ ] Identify the smallest useful smoke test that does not require external services beyond local Pi CLI availability.
- [ ] Add a script or documented manual checklist for starting the app and loading the main page.
- [ ] Consider a focused browserless check for static assets after CSS/JS extraction.
- [ ] Consider optional integration tests guarded by an env var so normal test runs remain fast and deterministic.
- [ ] Document how to run the smoke checks.

## Known existing product TODOs

This file is for production hardening/refactoring. Product and UX tasks may still live in `TODO.md`, including:

- scoped temporary project session expansion,
- slash command compatibility,
- remaining manual verification for scrolling and polling behavior,
- other user-facing enhancements.

Before starting a task, check whether `TODO.md` has related context so implementation does not drift.
