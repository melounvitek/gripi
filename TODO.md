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
