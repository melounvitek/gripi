# TODO

Persistent project TODOs for future sessions.

## How to use this file

When starting a new session, say:

> Address the next open item in TODO.md

The agent should:

1. Read this file first.
2. Pick the first unchecked actionable item, unless the user names a specific item.
3. Review the related context and inspect the relevant files before proposing changes.
4. Propose a small implementation plan before editing.
5. Mark checklist items complete only after the work is implemented and verified.
6. If code changes affect the running web gateway, tell the user a restart is needed; do not restart it unless explicitly asked.

---

## Bug: Keep project session expansion scoped and temporary

### Context

Clicking "Show all" in a project session group currently adds expanded cwd parameters to the URL. That expansion can persist across reloads and no-full-reload session switches, and it can make multiple project groups appear expanded. This makes the sidebar noisy and surprising.

Relevant starting points likely include:

- `views/_sidebar.erb`
- `views/_conversation.erb`
- `app.rb` sidebar helpers and session redirect/fragment URLs
- frontend session-switching code in `views/index.erb`

### Goal

"Show all" should affect only the intended project group, should not feel permanent after a reload, and should scale better for large projects.

### Checklist

- [x] Inspect how `expanded_cwd` is carried through sidebar links, new-session forms, redirects, and session fragments.
- [x] Make expansion scoped to the clicked project group only.
- [x] Prevent project expansion from persisting unexpectedly across full reloads.
- [x] Consider pagination or incremental loading instead of rendering every session for large project groups.
- [x] Verify session switching, new session creation, browser reload, and back/forward behavior.

---

## Feature: Align web aesthetics with Pi CLI

### Context

The web gateway should visually feel closer to Pi's CLI/TUI experience. The initial focus is mostly colors, not a larger redesign. Future sessions should inspect the existing web styles/templates and compare them with the Pi CLI/TUI color palette where possible.

Relevant starting points likely include:

- `views/`
- `app.rb`
- any CSS or inline style definitions used by the web UI

### Goal

The web interface uses a color palette and visual tone that better matches Pi in the CLI, while keeping the existing web layout and interactions stable.

### Checklist

- [x] Inspect the current web UI styling and identify where colors are defined.
- [x] Inspect Pi CLI/TUI theme colors or documented defaults for reference.
- [x] Propose a minimal palette mapping for the web UI.
- [x] Apply the palette with the smallest practical styling change.
- [x] Verify the UI still renders correctly.
- [x] Note whether a gateway restart is needed.

### Notes

- Keep this mostly to colors unless the user explicitly asks for broader visual redesign.
- Prefer a small, easy-to-review diff.

---

## Feature: Improve automatic session scrolling

### Context

Automatic session scrolling mostly works, but can get stuck when there are many tool calls or rapid updates. The UI needs a more robust mechanism that keeps the newest updates visible at the bottom during active generation.

Important behavior detail:

- Normally, while auto-scroll is active, the viewport should stay pinned to the newest updates at the bottom.
- Exception: if the latest model reply is taller than the visible screen area, the viewport should align the top of that latest reply with the top of the screen so the user can start reading from the beginning without manually scrolling back up.

Relevant starting points likely include:

- session/message rendering templates in `views/`
- frontend JavaScript responsible for polling, streaming, updating messages, or scrolling
- any CSS affecting message container height/overflow behavior

### Goal

The session view reliably follows new content during active updates, including bursts of tool calls, while presenting long final assistant messages from their beginning when they exceed the viewport height.

### Checklist

- [x] Inspect the current session rendering and scroll-management code.
- [x] Identify when auto-scroll currently decides it is active or inactive.
- [x] Reproduce or reason through the stuck-scroll case with many tool calls.
- [x] Design a robust scroll policy for active updates.
- [x] Implement bottom-pinning for normal active updates.
- [x] Implement the long-latest-reply exception: align the latest model reply's top with the viewport top when that reply is taller than the visible area.
- [x] Ensure user-initiated upward scrolling is not immediately overridden if the user intentionally leaves the bottom.
- [x] Verify behavior for rapid tool-call updates and long assistant replies.
- [x] Note whether a gateway restart is needed.

### Notes

- Be careful not to create scroll jitter.
- Prefer requestAnimationFrame or another DOM-settled timing mechanism if scroll calculations happen before layout is complete.
- Future sessions should preserve the user's ability to manually scroll up and read older content.
- Implemented explicit auto-scroll state, double `requestAnimationFrame` post-layout scroll scheduling, and a tall latest-assistant-message top-alignment exception. Automated tests pass.
- Gateway restart is needed for the deployed web UI to pick up these frontend changes.

---

## Feature: Support Pi CLI slash commands in the web gateway

### Context

The web gateway should investigate whether it can automatically support all slash commands that Pi exposes in the CLI/TUI, including commands such as `/stop`, `/new`, and any others visible in the CLI. The goal is broad compatibility with Pi's existing command surface rather than manually reimplementing only a small subset.

Future sessions should inspect how Pi defines, discovers, parses, and executes slash commands, then compare that with how the web gateway currently handles user input and session control.

Relevant starting points likely include:

- web gateway request/input handling in `app.rb`
- session-management code in `lib/`
- Pi CLI/TUI documentation and source for slash command definitions and behavior
- current web UI input handling in `views/`

### Goal

Determine and, if practical, implement a way for the web gateway to support the same slash commands available in Pi CLI, ideally by reusing or delegating to Pi's command handling rather than duplicating command logic.

### Checklist

- [ ] Inspect current web gateway handling for slash-prefixed user input.
- [ ] Inspect Pi CLI/TUI slash command definitions, discovery, parsing, and execution paths.
- [ ] List all Pi CLI slash commands that should be supported by the web gateway.
- [ ] Identify which commands are simple message/session commands and which require special web behavior, such as `/stop` or `/new`.
- [ ] Determine whether commands can be supported automatically by reusing Pi internals or whether explicit web mappings are required.
- [ ] Propose the smallest safe implementation strategy.
- [ ] Implement command support for the agreed scope.
- [ ] Add or update tests for command handling where practical.
- [ ] Verify important commands manually, especially `/stop` and `/new`.
- [ ] Note whether a gateway restart is needed.

### Notes

- Prefer automatic reuse of Pi's command registry/handler if available.
- Avoid drifting behavior from the CLI; if a command cannot behave identically in the web UI, document the difference before implementing.
- Be careful with commands that affect session lifecycle, cancellation, filesystem state, or process state.

---

## Feature: Investigate `/events` polling resilience and performance

### Context

The web gateway currently polls `/events?session=...` once per second from each open session page. The endpoint drains asynchronous Pi RPC events from the session's `PiRpcClient` and returns them to the browser for live rendering.

A recent gateway hang looked like Puma thread saturation while serving repeated `/events` requests. Future sessions should investigate whether polling can be made safer and more efficient so slow or stuck RPC clients, multiple open tabs, or overlapping browser polls cannot wedge the web server.

Relevant starting points likely include:

- `/events` route in `app.rb`
- `PiRpcClient#drain_events` and RPC reader behavior in `lib/pi_rpc_client.rb`
- client polling loop in `views/index.erb`
- Puma thread configuration and request timeout behavior

### Goal

Make live event delivery robust enough that the web UI remains responsive even when Pi RPC clients are slow, stuck, idle, or polled from multiple browser tabs.

### Checklist

- [x] Inspect current `/events` server behavior and confirm whether it can create or remap RPC clients during polling.
- [x] Inspect the browser polling loop for overlapping requests, retry behavior, and behavior across multiple open tabs.
- [x] Reproduce or reason through how `/events` requests can occupy all Puma threads.
- [x] Evaluate simple client-side mitigations such as preventing overlapping polls, backing off while idle, and pausing in hidden tabs.
- [x] Evaluate server-side mitigations such as fast nonblocking drains, request timeouts, stale client cleanup, and avoiding RPC client creation from passive polling.
- [x] Consider whether Server-Sent Events, WebSockets, or a single shared poll loop would be a better long-term fit.
- [x] Propose the smallest safe improvement before implementation.
- [x] Add or update tests where practical.
- [ ] Verify the page stays responsive under multiple open tabs and idle sessions.
- [ ] Evaluate follow-up mitigations for very large active `/events` responses, such as event batch limits or response compression.
- [x] Note whether a gateway restart is needed.

### Notes

- Preserve near-real-time live output during active generation.
- Prefer small changes that reduce the chance of Puma thread starvation before attempting a larger transport redesign.
- Avoid losing events when changing drain or polling behavior.

---

## Feature: Improve edit tool-call summaries

### Context

Some rendered edit tool calls do not show useful information when expanded in the web UI. In the reported example, an `edit TODO.md` tool call only shows a generic `edit TODO.md` line plus `Raw details`, which does not make the actual change understandable at a glance.

The collapsed/expanded tool-call card should provide a helpful summary for edit operations, ideally including what file changed and enough of the replacement/diff context to understand the edit without opening raw JSON details.

Relevant starting points likely include:

- tool-call rendering templates in `views/`
- tool-call formatting or summarization helpers in `lib/`
- any frontend code that expands/collapses tool-call details
- examples of rendered `edit` tool calls in session data

### Goal

Edit tool-call cards provide useful human-readable information when opened, so the user can quickly understand what was changed without inspecting raw details.

### Checklist

- [x] Inspect how tool calls are currently rendered and summarized.
- [x] Inspect the stored structure for `edit` tool-call arguments/results.
- [x] Identify why `edit` currently shows only generic information.
- [x] Design a concise edit summary format, such as file path plus changed snippets or a small diff-like preview.
- [x] Implement the summary with safe truncation for large edits.
- [x] Preserve access to raw details for debugging.
- [x] Verify rendering for simple single-file edits and multi-edit calls.
- [x] Note whether a gateway restart is needed.

### Notes

- Avoid dumping huge edit payloads into the normal card body.
- Prefer a stable, readable preview over raw JSON.
- Handle missing or malformed edit arguments gracefully.

---

## Feature: Change model and thinking mode from the footer

### Context

The web gateway footer shows the current model and thinking setting. These should become interactive controls: the user should be able to click the model or thinking value in the footer and change them without leaving the current session view.

Future sessions should inspect how the current model/thinking values are displayed, where session defaults or runtime options are stored, and how new prompts are submitted with those settings.

Relevant starting points likely include:

- footer rendering in `views/`
- frontend JavaScript for session controls and form submission
- server endpoints in `app.rb`
- session configuration/state code in `lib/`
- any existing model or thinking selection behavior elsewhere in the app

### Goal

The footer provides a clear, low-friction way to change the active model and thinking setting for subsequent requests in the current session, while accurately reflecting the current effective values.

### Checklist

- [ ] Inspect how the footer currently renders model and thinking information.
- [ ] Inspect where model and thinking settings are stored and passed to Pi.
- [ ] Determine whether changes should affect only the current session, new sessions, or both.
- [ ] Design the footer interaction, such as a dropdown, popover, or inline selector.
- [ ] Ensure available model and thinking options come from the same source as Pi or existing app configuration where possible.
- [ ] Implement model selection from the footer.
- [ ] Implement thinking-mode selection from the footer.
- [ ] Ensure the selected values are used for subsequent user messages.
- [ ] Verify the footer updates after changes and remains usable on small screens.
- [ ] Note whether a gateway restart is needed.

### Notes

- Avoid making the footer visually noisy; keep the controls subtle until clicked.
- Be clear whether changes apply immediately to the next prompt or require a new session.
- Prefer reusing existing configuration/model lists instead of hard-coding options if possible.

---

## Feature: Search sessions

### Context

The archived usability plan lists session search as a later idea. As the number of Pi sessions grows, browsing by cwd groups alone may become too slow. The web gateway should investigate a lightweight search experience for finding sessions by directory, title, and possibly message contents.

Relevant starting points likely include:

- sidebar/session list rendering in `views/`
- session discovery and metadata in `lib/`
- routes in `app.rb` that load and filter sessions

### Goal

Make it quick to find an existing session without manually scanning cwd groups.

### Checklist

- [x] Inspect current session metadata and sidebar filtering/grouping.
- [x] Decide search scope: cwd, session title, first user message, and/or full message text.
- [x] Decide whether search should be client-side, server-side, or hybrid.
- [x] Design a compact search UI that does not clutter the sidebar.
- [x] Implement the approved search behavior.
- [x] Verify search with many sessions and long cwd paths.
- [x] Note whether a gateway restart is needed.

### Notes

- Prefer metadata-only search first unless full-text search is clearly needed.
- Avoid adding a database just for search unless file scanning proves too slow.
- Implemented as server-side metadata search over cwd/project label, session title, and first user message; full message text is intentionally deferred.
- Gateway restart is needed for the deployed web UI to pick up these server-rendered sidebar changes.

---

## Feature: Syntax highlighting for code blocks

### Context

Assistant markdown already renders code blocks, but the archived usability plan lists syntax highlighting as a later polish item. Highlighted code could improve readability for longer technical answers and tool-related snippets.

Relevant starting points likely include:

- markdown rendering helpers/endpoints in `app.rb`
- CSS in `views/`
- existing markdown sanitization behavior

### Goal

Render fenced code blocks with readable syntax highlighting while preserving safe sanitized HTML output.

### Checklist

- [ ] Inspect the current markdown rendering and sanitization pipeline.
- [ ] Evaluate a small Ruby-side or browser-side syntax highlighting approach.
- [ ] Ensure highlighted output remains sanitized and safe.
- [ ] Add styling compatible with the current dark theme.
- [ ] Implement syntax highlighting for common fenced code languages.
- [ ] Verify unknown languages and plain code blocks still render cleanly.
- [ ] Note whether a gateway restart is needed.

### Notes

- Do not add a large frontend build pipeline just for highlighting.
- Preserve copyability and readability of code blocks.

---

## Feature: Reconcile messages after completed turns

### Context

The archived usability plan lists reconciling from `get_messages` after each completed turn as a later robustness idea. The browser currently renders live output incrementally from RPC events, while Pi session files remain the source of truth. A completed turn could be reconciled against Pi's authoritative final messages to avoid live-rendering drift.

Relevant starting points likely include:

- live event rendering in `views/`
- `/events` and session message routes in `app.rb`
- `PiRpcClient#get_messages`
- historical message rendering helpers

### Goal

After Pi finishes a turn, the web transcript should match the final Pi session state without requiring a manual page refresh.

### Checklist

- [ ] Inspect how live-rendered messages can differ from historical/session-file messages.
- [ ] Decide whether reconciliation should call RPC `get_messages`, re-read JSONL, or use another source.
- [ ] Decide whether to re-render the full conversation area or patch only the latest turn.
- [ ] Preserve scroll position and auto-scroll behavior during reconciliation.
- [ ] Implement the approved reconciliation strategy after `turn_end` / `agent_end`.
- [ ] Verify live transcript drift, missed events, and duplicate live cards are corrected after completion.
- [ ] Note whether a gateway restart is needed.

### Notes

- Avoid a disruptive full-page refresh if a targeted transcript refresh is practical.
- Pi remains the source of truth; the browser should not invent final message state.

---

## Feature: Add date separators in conversations

### Context

The archived usability plan lists date separators as a later idea. Long sessions can span multiple days, and subtle separators could make older conversation history easier to scan.

Relevant starting points likely include:

- historical message rendering in `views/`
- live message append logic in frontend JavaScript
- timestamp formatting helpers in `app.rb` and `views/`

### Goal

Show unobtrusive date separators between messages from different days, without making conversations visually noisy.

### Checklist

- [ ] Inspect available timestamps for historical and live messages.
- [ ] Decide separator granularity and formatting.
- [ ] Implement date separators for historical rendering.
- [ ] Implement date separators for live-appended messages if needed.
- [ ] Verify behavior across same-day and multi-day sessions.
- [ ] Note whether a gateway restart is needed.

### Notes

- Keep this lower priority than navigation, reliability, and session-management improvements.
- Avoid separators when timestamps are missing or unreliable.
