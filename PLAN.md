# Terminal-aware tool output

## Goal

Render read-only terminal streams as their current screen state, including common ANSI colors and styles, without changing ordinary tool output or Pi-owned data formats.

## TDD rounds

1. Add a bounded terminal parser/DOM model backed by a pinned, unmounted xterm parser. Cover carriage returns, clear/home, cursor and erase commands, Unicode, ANSI styles, unsafe OSC sequences, and plain-text bypass.
2. Integrate terminal rendering with live cumulative bash output. Coalesce asynchronous updates, reject stale output, preserve canonical results, and keep read/edit/write behavior unchanged.
3. Encode and hydrate terminal-controlled server-rendered and lazily loaded history through the same renderer. Keep collapse, find, copy, and viewport behavior based on displayed screen text.
4. Add theme styling, lifecycle and bounds coverage, and browser-level fake-Pi acceptance coverage.
5. Run focused and full validation, then independent simplification and philosophy reviews. Address actionable findings and rerun review.

## Non-goals

- Interactive keyboard, mouse, or clipboard forwarding
- A mounted browser terminal widget
- tmux-specific gateway behavior
- Reconnect restoration beyond the existing live-event lifecycle
- Changes to Pi session or tool-result formats
