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
- [ ] Verify behavior for rapid tool-call updates and long assistant replies.
- [x] Note whether a gateway restart is needed.

### Notes

- Be careful not to create scroll jitter.
- Prefer requestAnimationFrame or another DOM-settled timing mechanism if scroll calculations happen before layout is complete.
- Future sessions should preserve the user's ability to manually scroll up and read older content.
- Implemented explicit auto-scroll state, double `requestAnimationFrame` post-layout scroll scheduling, and a tall latest-assistant-message top-alignment exception. Automated tests pass; manual browser verification for rapid tool bursts and long replies remains open.
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

## Feature: Plan web notifications with snooze

### Context

The web gateway should investigate whether notifications are possible and useful, then plan what events should trigger them. Notifications should include a snooze mechanism so the user can temporarily suppress notification noise without disabling the feature permanently.

This is a planning-first item: future sessions should evaluate browser notification APIs, permission UX, server/client event availability, and user experience before implementation.

Potential notification events to consider:

- assistant finished responding
- assistant is waiting for user input or approval
- tool call failed or needs attention
- long-running command completed
- session encountered an error or disconnected
- gateway/server status changed if detectable from the web UI

Relevant starting points likely include:

- frontend JavaScript in `views/`
- session polling or streaming update code
- server endpoints in `app.rb`
- any session state or event metadata in `lib/`

### Goal

Produce and, if approved, implement a notification system that alerts the user about important session events, avoids noisy or duplicate notifications, and supports snoozing notifications for a chosen period.

### Checklist

- [ ] First establish a valid HTTPS origin for the gateway, ideally via Tailscale MagicDNS/custom port or a reverse proxy, because browser notifications and PWA push require a secure context.
- [x] Inspect how the web UI currently detects session state changes and new messages.
- [x] Determine whether browser notifications are sufficient or whether another mechanism is needed.
- [x] Identify which events are worth notifying about and which would be too noisy.
- [x] Design notification permission flow and fallback behavior when permission is denied or unavailable.
- [x] Design snooze behavior, including available durations, persistence, and how to unsnooze.
- [x] Define duplicate-suppression rules so repeated polling does not resend the same notification.
- [x] Propose a minimal first implementation scope.
- [ ] Implement the approved notification behavior.
- [ ] Verify notifications, snooze, duplicate suppression, and permission-denied behavior.
- [ ] Note whether a gateway restart is needed.

### Notes

- Prefer opt-in notifications; do not surprise the user with permission prompts on page load.
- HTTPS is the first implementation prerequisite. Without it, desktop browser notifications are unlikely to work from the current `http://100.103.198.74:4567` origin, and iPhone Home Screen/PWA push will not work.
- For iPhone Home Screen usage, plan for PWA-compatible notifications: manifest/service worker first, then Web Push if notifications must work after the app is closed.
- Snooze should suppress non-critical notifications, but critical errors may need separate consideration.
- Keep notification text concise and avoid exposing sensitive prompt or tool-output details unnecessarily.

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

## Feature: Investigate keyboard shortcuts for session navigation

### Context

The web gateway should investigate adding keyboard shortcuts for faster session navigation. Desired shortcuts include:

- `Cmd+K` / `Ctrl+K` to open or focus recent sessions.
- `Ctrl+1`, `Ctrl+2`, etc. to switch between sessions.

Future sessions should inspect the current session list/navigation UI, how sessions are ordered, and how keyboard events are currently handled, if at all.

Relevant starting points likely include:

- session list/navigation rendering in `views/`
- frontend JavaScript for global event handling
- server routes in `app.rb` for session pages
- any existing recent-session data or ordering logic in `lib/`

### Goal

Determine whether useful keyboard shortcuts can be added safely, then implement a minimal shortcut set for opening recent sessions and switching between visible/recent sessions.

### Checklist

- [ ] Inspect current session navigation and recent-session UI behavior.
- [ ] Inspect whether the web app already has keyboard shortcut handling.
- [ ] Decide what `Cmd+K` / `Ctrl+K` should open or focus.
- [ ] Decide which session ordering `Ctrl+1`, `Ctrl+2`, etc. should use.
- [ ] Check for conflicts with browser/system shortcuts and text input behavior.
- [ ] Design shortcut behavior that does not trigger while typing in prompts or forms, except where intentional.
- [ ] Implement the approved shortcut behavior.
- [ ] Add discoverability, such as tooltip text or a small shortcuts hint, if appropriate.
- [ ] Verify shortcuts on macOS-style `Cmd` and non-macOS `Ctrl` flows where practical.
- [ ] Note whether a gateway restart is needed.

### Notes

- Avoid hijacking browser shortcuts unexpectedly.
- Shortcut behavior should be predictable when a modal, menu, or prompt textarea is focused.
- Prefer a small initial shortcut set over a broad shortcut framework.

---

## Feature: Investigate Tab focus behavior between input and session view

### Context

The web gateway should investigate whether `Tab` behavior can be customized for the main session view. Desired behavior:

- When focus is in the input box, pressing `Tab` should move focus to the session/messages area above it.
- Once the session/messages area is focused, arrow keys and `Page Up` / `Page Down` should immediately scroll or navigate through the messages.
- Pressing `Tab` again should move focus back into the input box for typing.
- In the main session flow, `Tab` should only toggle between these two places: the input box and the session/messages area.

Future sessions should evaluate browser accessibility expectations and avoid breaking normal keyboard access to other interactive controls unless there is a clear alternative.

Relevant starting points likely include:

- input form and session message container templates in `views/`
- frontend JavaScript keyboard/focus handling
- CSS for focus outlines and scrollable containers
- any existing keyboard shortcut code from the session navigation shortcut work

### Goal

Determine whether this two-position `Tab` focus loop is feasible and accessible, then implement it if approved so keyboard navigation between typing and reading messages feels fast and predictable.

### Checklist

- [ ] Inspect current focus order around the prompt input, session/messages area, footer, and other controls.
- [ ] Determine whether the session/messages container can receive focus and handle arrow/page scrolling naturally.
- [ ] Check accessibility tradeoffs of overriding normal `Tab` navigation.
- [ ] Design a focus loop between the input box and session/messages area, including an escape path for other controls if needed.
- [ ] Ensure `Tab` from the input focuses the correct message/session container.
- [ ] Ensure arrow keys and `Page Up` / `Page Down` work immediately after focusing the session area.
- [ ] Ensure `Tab` from the session area returns focus to the input box.
- [ ] Verify behavior does not interfere with text entry, selection, browser shortcuts, or screen-reader expectations more than necessary.
- [ ] Note whether a gateway restart is needed.

### Notes

- This is related to keyboard shortcuts, but should be considered separately because it changes fundamental focus behavior.
- Keep a visible focus indication so the user can tell whether typing or message navigation is active.
- Consider whether `Shift+Tab` should mirror or bypass the loop before implementing.

---

## Feature: Investigate session switching without full page reloads

### Context

Opening another session, whether an existing session or a newly created one, should ideally avoid a full page reload. The preferred direction is to consider HTMX where it makes sense, but not to use HTMX blindly if a simpler or more robust approach fits better.

Future sessions should inspect how session navigation and session creation currently work, what parts of the page actually need to change, and whether the existing server-rendered views can be partially updated cleanly.

Relevant starting points likely include:

- session navigation links/forms in `views/`
- routes for opening existing sessions and creating new sessions in `app.rb`
- session list/sidebar rendering, if separate from the main session view
- frontend JavaScript that handles current session state, polling, scrolling, input focus, message updates, and live session rename propagation
- any existing HTMX usage or dependency setup

### Goal

Determine whether session open/create flows can be made smoother without full page reloads, then implement a minimal approach that updates only the necessary UI while preserving correct URL/history, session state, polling, focus, scrolling behavior, and live propagation of session renames.

### Checklist

- [x] Inspect current session open and new-session flows to identify what triggers full page reloads.
- [x] Identify which page regions must update when switching sessions or creating a new one.
- [x] Check whether HTMX is already available or would need to be introduced.
- [x] Weigh HTMX pros and cons for this app versus plain JavaScript or keeping full reloads.
- [x] Decide how browser history, back/forward navigation, and deep links should behave.
- [x] Decide how polling, auto-scroll state, input contents, and focus should reset or transfer across session switches.
- [ ] Ensure session rename events update the visible session title/sidebar state without a manual refresh.
- [x] Propose the smallest safe implementation strategy.
- [x] Implement the approved no-full-reload behavior for existing session switches.
- [x] Implement the approved no-full-reload behavior for creating/opening a new session.
- [ ] Verify direct links, browser back/forward, refresh, and rapid session switching.
- [x] Note whether a gateway restart is needed.

### Notes

- HTMX is a preferred option to evaluate, not a requirement.
- Avoid duplicating large amounts of rendering logic between server and client.
- Do not sacrifice reliable session state or shareable URLs just to avoid reloads.
- Implemented with plain JavaScript and server-rendered sidebar/conversation fragments. Existing session clicks and sidebar new-session form submissions fetch fragments, replace only the sidebar/conversation regions, update history, reset polling/live state/attachments/scroll state, close the mobile sidebar, and preserve expanded cwd query parameters. Automated tests pass; manual browser verification remains open. Gateway restart is needed for the running web UI to pick up these Sinatra/template/frontend changes.

---

## Feature: Add a scroll-up button

### Context

The web gateway already has a scroll-down button for quickly returning to newer content. It should also investigate adding a similar scroll-up button so the user can quickly jump upward in the session, likely toward older messages or the top of the current session view.

Future sessions should inspect the existing scroll-down button behavior and reuse its visual style, placement, visibility logic, and accessibility patterns where appropriate.

Relevant starting points likely include:

- frontend JavaScript that manages the existing scroll-down button
- session/message templates in `views/`
- CSS for floating scroll controls
- auto-scroll behavior and session scrolling logic

### Goal

Provide a matching scroll-up control that helps users navigate long sessions without fighting the existing auto-scroll and scroll-down behavior.

### Checklist

- [ ] Inspect the existing scroll-down button implementation and visibility rules.
- [ ] Decide what scroll-up should do: jump to top, jump one viewport, or jump to previous important message boundary.
- [ ] Decide when the scroll-up button should be visible or hidden.
- [ ] Reuse the existing scroll button styling and accessibility approach where possible.
- [ ] Implement the approved scroll-up behavior.
- [ ] Verify interaction with auto-scroll, manual scrolling, and the existing scroll-down button.
- [ ] Verify behavior on long sessions and small screens.
- [ ] Note whether a gateway restart is needed.

### Notes

- Keep the behavior predictable and avoid adding visual clutter.
- Consider whether the button should appear only after the user has scrolled down far enough from the top.
- Ensure it does not interfere with the prompt input or floating scroll-down control.

---

## Feature: Show current directory and branch in the status bar

### Context

The web gateway status bar should show the current working directory and Git branch for the active session. This should make it easier to understand where the agent is operating without asking or inspecting logs.

Future sessions should inspect what session metadata is already available in the web UI and whether directory/branch data should be read from Pi session state, server-side process state, or a lightweight Git lookup.

Relevant starting points likely include:

- status bar/footer rendering in `views/`
- session metadata/state code in `lib/`
- routes or serializers in `app.rb` that provide session updates to the frontend
- any existing code that detects or displays model/thinking/session status

### Goal

The status bar clearly displays the active session's current directory and Git branch when available, with graceful fallback for non-Git directories or unknown state.

### Checklist

- [ ] Inspect current status bar/footer rendering and available session metadata.
- [ ] Determine the reliable source for current directory and branch.
- [ ] Decide how to handle non-Git directories, detached HEAD, and unknown branch state.
- [ ] Design concise status bar text that does not crowd existing controls.
- [ ] Implement directory and branch display.
- [ ] Verify values update correctly when switching sessions or directories.
- [ ] Note whether a gateway restart is needed.

### Notes

- Prefer showing shortened but unambiguous paths if space is limited.
- Avoid expensive Git checks on every poll if cached/session metadata is available.

---

## Feature: Brainstorm unread session highlighting

### Context

The web gateway should investigate whether sessions with unread messages can be highlighted in the sidebar, and whether unread status should be propagated elsewhere in the UI. One possible design is adding an `Unread` section above the directory/session groups so sessions needing attention are visible immediately.

This is a brainstorming/planning item first. Future sessions should define what counts as unread in this app, how read state should be tracked, and how to avoid noisy or confusing indicators.

Potential unread events to consider:

- assistant finished responding in a session that is not currently active
- a tool call failed or needs attention in a background session
- a session is waiting for user approval/input while not active
- new output arrives after the user last viewed that session

Relevant starting points likely include:

- sidebar/session list rendering in `views/`
- session update/polling code in frontend JavaScript
- session state and message timestamps in `lib/`
- routes in `app.rb` that return session lists or session metadata

### Goal

Determine whether unread highlighting is feasible and useful, then propose a minimal design for tracking, displaying, and clearing unread state across sessions.

### Checklist

- [ ] Inspect current sidebar grouping and session metadata.
- [ ] Define what should count as an unread session event.
- [ ] Decide when unread state should be cleared, such as opening the session or scrolling to the newest message.
- [ ] Decide whether unread state should be persisted across page reloads/server restarts or only kept client-side.
- [ ] Evaluate UI options: badges, row highlighting, an `Unread` section above directories, or status bar indicators.
- [ ] Consider how unread highlighting interacts with notifications and no-reload session switching work.
- [ ] Propose the smallest useful implementation.
- [ ] Implement the approved unread highlighting behavior.
- [ ] Verify unread state across active/background sessions and session switches.
- [ ] Note whether a gateway restart is needed.

### Notes

- Avoid duplicating sessions confusingly if an `Unread` section is added above directory groups.
- Keep the indicator calm and readable; unread should guide attention, not create noise.
- Be explicit about whether unread is per-browser, per-session, or globally persisted.

---

## Bug: Resume mobile sessions after browser closes

### Context

On mobile, if the user closes the browser or tab while a session is open and later returns, the previously opened session can get stuck and no longer appears to refresh. The page may still show the session, but live polling or session state does not recover as expected after the mobile browser suspends or discards the page.

This likely involves browser lifecycle behavior on mobile, such as background suspension, bfcache restores, stale polling timers, or stale RPC/session state after reconnecting.

Relevant starting points likely include:

- frontend polling and visibility/page lifecycle handlers in `views/`
- `/events` polling behavior in `app.rb`
- active RPC client/session handling
- browser events such as `visibilitychange`, `pageshow`, `pagehide`, `focus`, and network reconnects

### Goal

Returning to an open session on mobile should reliably resume refreshing when practical. If automatic recovery is too complex or unreliable, the UI should clearly inform the user that the session may be stale and provide an obvious refresh/reconnect button so recovery is one tap away.

### Checklist

- [x] Reproduce or reason through the mobile close/return lifecycle that leaves a session stuck.
- [x] Inspect current event polling timer setup and whether it resumes after page restore.
- [x] Inspect whether stale in-flight polling state can block future polls after suspension.
- [x] Decide whether to restart polling, refresh session state, reload the page, or show a manual recovery prompt on mobile/page restore.
- [x] Implement the smallest safe recovery behavior, with a visible stale-session message and refresh/reconnect button if automatic recovery is not chosen.
- [ ] Verify returning to an open session after tab close/backgrounding on mobile.
- [x] Verify desktop polling behavior is unchanged.
- [x] Note whether a gateway restart is needed.

### Notes

- Prefer a targeted lifecycle recovery over increasing polling frequency.
- A manual refresh/reconnect affordance is acceptable if automatic refresh would be fragile or overly complicated.
- Avoid creating duplicate overlapping poll loops after repeated hide/show cycles.
- Preserve the selected session and input contents where possible.
- Implemented automatic resume recovery by aborting stale event polls, restarting polling on mobile/browser lifecycle resume, refreshing the current session when events were missed, and showing a manual reconnect banner if polling cannot recover. Draft text and attached image files are preserved across same-session reconnects. Automated tests pass; manual mobile background/return verification remains open. Gateway restart is needed for the running web UI to pick up the frontend/template changes.

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

- [ ] Inspect current session metadata and sidebar filtering/grouping.
- [ ] Decide search scope: cwd, session title, first user message, and/or full message text.
- [ ] Decide whether search should be client-side, server-side, or hybrid.
- [ ] Design a compact search UI that does not clutter the sidebar.
- [ ] Implement the approved search behavior.
- [ ] Verify search with many sessions and long cwd paths.
- [ ] Note whether a gateway restart is needed.

### Notes

- Prefer metadata-only search first unless full-text search is clearly needed.
- Avoid adding a database just for search unless file scanning proves too slow.

---

## Feature: Pin or favorite sessions

### Context

The archived usability plan lists pinned/favorite sessions as a later idea. Some sessions are long-running or frequently revisited and should be easier to access than regular recency sorting allows.

Relevant starting points likely include:

- sidebar/session list rendering in `views/`
- session metadata/state code in `lib/`
- any existing persisted local gateway state, if added later

### Goal

Allow important sessions to stay visible and easy to reopen, even when they are older than the latest sessions in their cwd group.

### Checklist

- [ ] Decide what persistence mechanism should store pins/favorites.
- [ ] Decide whether favorites are global, per-browser, or local-machine specific.
- [ ] Design how pinned sessions appear in the sidebar.
- [ ] Implement pin/unpin behavior.
- [ ] Ensure pinned sessions still work with cwd grouping and selected-session visibility.
- [ ] Verify pins survive page reloads and server restarts if persistence is intended.
- [ ] Note whether a gateway restart is needed.

### Notes

- Avoid confusing duplication if a pinned section also shows sessions in cwd groups.
- Keep this local/personal unless a broader state model is introduced.

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
