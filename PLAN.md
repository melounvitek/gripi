# Bounded terminal transcripts

## Goal

Show terminal output as a bounded, browser-scrollable transcript: preserved terminal scrollback followed by the current active screen. Keep the behavior general to ANSI/VT streams and cumulative terminal snapshots rather than adding tmux-specific rendering.

## Constraints

- Preserve the existing plain-output fast path and `read`/`edit`/`write` behavior.
- Keep canonical tool results authoritative across live output and restored history.
- Keep input, geometry, scrollback, parser lifetime, and DOM output bounded.
- Do not attempt to reconstruct content that the source terminal discarded without exposing as scrollback.
- Do not add interactive keyboard, mouse, clipboard, title, link, or terminal RPC behavior.
- Do not merge this branch.

## TDD rounds

- [x] Extract normal-buffer scrollback together with an active alternate screen, with explicit output bounds and generic reset-snapshot coverage.
- [x] Verify cumulative transcript snapshots use the shared live and restored rendering paths without duplicate or stale history.
- [x] Add browser coverage for expanding, scrolling through, styling, and restoring a bounded transcript.
- [ ] Run focused and full validation, browser verification with a screenshot, and independent simplification/philosophy reviews.

## Producer contract

A source that can provide cumulative terminal history may publish full snapshots using standard terminal controls:

```text
CSI 3 J + CSI 2 J + CSI H + history and active screen
```

For example, a tmux transport can capture history with `capture-pane -p -e -S -`. This remains a transport concern; the renderer only interprets standard terminal semantics.

## Non-goals

- Unbounded transcript retention or download archives
- tmux-specific renderer branches or a permanent tmux subagent transport
- Historical visual frames overwritten inside alternate-screen TUIs
- Reconnect restoration for an unfinished tool execution
