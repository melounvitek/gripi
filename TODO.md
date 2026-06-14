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

- [ ] Inspect the current web UI styling and identify where colors are defined.
- [ ] Inspect Pi CLI/TUI theme colors or documented defaults for reference.
- [ ] Propose a minimal palette mapping for the web UI.
- [ ] Apply the palette with the smallest practical styling change.
- [ ] Verify the UI still renders correctly.
- [ ] Note whether a gateway restart is needed.

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
