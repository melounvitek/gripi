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

- [ ] Inspect the current session rendering and scroll-management code.
- [ ] Identify when auto-scroll currently decides it is active or inactive.
- [ ] Reproduce or reason through the stuck-scroll case with many tool calls.
- [ ] Design a robust scroll policy for active updates.
- [ ] Implement bottom-pinning for normal active updates.
- [ ] Implement the long-latest-reply exception: align the latest model reply's top with the viewport top when that reply is taller than the visible area.
- [ ] Ensure user-initiated upward scrolling is not immediately overridden if the user intentionally leaves the bottom.
- [ ] Verify behavior for rapid tool-call updates and long assistant replies.
- [ ] Note whether a gateway restart is needed.

### Notes

- Be careful not to create scroll jitter.
- Prefer requestAnimationFrame or another DOM-settled timing mechanism if scroll calculations happen before layout is complete.
- Future sessions should preserve the user's ability to manually scroll up and read older content.

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

- [ ] Inspect current `/events` server behavior and confirm whether it can create or remap RPC clients during polling.
- [ ] Inspect the browser polling loop for overlapping requests, retry behavior, and behavior across multiple open tabs.
- [ ] Reproduce or reason through how `/events` requests can occupy all Puma threads.
- [ ] Evaluate simple client-side mitigations such as preventing overlapping polls, backing off while idle, and pausing in hidden tabs.
- [ ] Evaluate server-side mitigations such as fast nonblocking drains, request timeouts, stale client cleanup, and avoiding RPC client creation from passive polling.
- [ ] Consider whether Server-Sent Events, WebSockets, or a single shared poll loop would be a better long-term fit.
- [ ] Propose the smallest safe improvement before implementation.
- [ ] Add or update tests where practical.
- [ ] Verify the page stays responsive under multiple open tabs and idle sessions.
- [ ] Note whether a gateway restart is needed.

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

- [ ] Inspect how tool calls are currently rendered and summarized.
- [ ] Inspect the stored structure for `edit` tool-call arguments/results.
- [ ] Identify why `edit` currently shows only generic information.
- [ ] Design a concise edit summary format, such as file path plus changed snippets or a small diff-like preview.
- [ ] Implement the summary with safe truncation for large edits.
- [ ] Preserve access to raw details for debugging.
- [ ] Verify rendering for simple single-file edits and multi-edit calls.
- [ ] Note whether a gateway restart is needed.

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

- [ ] Inspect how the web UI currently detects session state changes and new messages.
- [ ] Determine whether browser notifications are sufficient or whether another mechanism is needed.
- [ ] Identify which events are worth notifying about and which would be too noisy.
- [ ] Design notification permission flow and fallback behavior when permission is denied or unavailable.
- [ ] Design snooze behavior, including available durations, persistence, and how to unsnooze.
- [ ] Define duplicate-suppression rules so repeated polling does not resend the same notification.
- [ ] Propose a minimal first implementation scope.
- [ ] Implement the approved notification behavior.
- [ ] Verify notifications, snooze, duplicate suppression, and permission-denied behavior.
- [ ] Note whether a gateway restart is needed.

### Notes

- Prefer opt-in notifications; do not surprise the user with permission prompts on page load.
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
- frontend JavaScript that handles current session state, polling, scrolling, input focus, and message updates
- any existing HTMX usage or dependency setup

### Goal

Determine whether session open/create flows can be made smoother without full page reloads, then implement a minimal approach that updates only the necessary UI while preserving correct URL/history, session state, polling, focus, and scrolling behavior.

### Checklist

- [ ] Inspect current session open and new-session flows to identify what triggers full page reloads.
- [ ] Identify which page regions must update when switching sessions or creating a new one.
- [ ] Check whether HTMX is already available or would need to be introduced.
- [ ] Weigh HTMX pros and cons for this app versus plain JavaScript or keeping full reloads.
- [ ] Decide how browser history, back/forward navigation, and deep links should behave.
- [ ] Decide how polling, auto-scroll state, input contents, and focus should reset or transfer across session switches.
- [ ] Propose the smallest safe implementation strategy.
- [ ] Implement the approved no-full-reload behavior for existing session switches.
- [ ] Implement the approved no-full-reload behavior for creating/opening a new session.
- [ ] Verify direct links, browser back/forward, refresh, and rapid session switching.
- [ ] Note whether a gateway restart is needed.

### Notes

- HTMX is a preferred option to evaluate, not a requirement.
- Avoid duplicating large amounts of rendering logic between server and client.
- Do not sacrifice reliable session state or shareable URLs just to avoid reloads.
