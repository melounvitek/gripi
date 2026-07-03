# Pi Web Gateway Usability Plan

## Goal

Turn the current Pi Web Gateway PoC into a usable personal browser UI for daily Pi sessions.

Keep the same core architecture:

- Pi remains the source of truth for sessions and messages.
- Native JSONL session files stay authoritative.
- The web gateway owns only local browser UX and Pi RPC process orchestration.
- Avoid adding a database unless a later UX requirement truly needs it.

Target experience:

> Open the local web app, browse recent sessions by working directory, switch into a conversation that opens at the bottom, read assistant markdown comfortably, see Pi/tool noise compacted by default, and chat from a Discord-like layout.

## Step delivery check

After each implementation step:

1. Run focused automated tests for the changed behavior.
2. Restart the local Sinatra/Puma server so route/view/CSS/JS changes are loaded.
3. Verify the app manually at:

> http://100.x.y.z:4567/

4. Report the exact link to open, what changed, and what to test there.

## Current baseline

The completed PoC already has:

- Session discovery from `~/.pi/agent/sessions/**/*.jsonl`.
- Sessions grouped by cwd.
- Session selection and read-only message rendering.
- One active `pi --mode rpc --session <session-file>` process.
- Prompt, abort, compact, rename, and new-session actions.
- Basic live event polling.
- Basic slash command discovery.
- No database.

## Non-goals for this usability pass

- No production packaging.
- No authentication or multi-user support.
- No full Pi TUI parity.
- No database-backed conversation cache.
- No complex multi-session background process manager unless the UX work proves one active RPC process is too limiting.

## Implementation checklist

### 1. Discord-like app shell and scrolling

- [x] Split the page into a viewport-height app shell.
- [x] Make the left sidebar scroll independently.
- [x] Make the conversation/message area scroll independently.
- [x] Keep the session header visible while reading.
- [x] Keep the prompt composer fixed/sticky at the bottom of the conversation panel.
- [x] Ensure opening a selected conversation scrolls to the bottom.
- [x] Ensure live output keeps the view at the bottom when the user is already near the bottom.
- [x] Add or update tests where practical for expected structure/classes.
- [x] Restart server and report the test URL plus what to verify.

Success check:

- Sidebar and conversation can be scrolled separately.
- Opening a long conversation lands near the newest messages/composer.
- Sending a prompt does not require scrolling back to the input.

### 2. Sidebar session trimming and cwd navigation

- [x] Show only the latest 5 sessions for each cwd by default.
- [x] Keep sessions sorted newest-first inside each cwd.
- [x] Add a simple “show all” or “show more” affordance per cwd.
- [x] Make cwd groups visually compact and readable for long paths.
- [x] Preserve selected-session visibility even if it is older than the latest 5.
- [x] Add tests for default trimming and selected-session preservation.
- [x] Restart server and report the test URL plus what to verify.

Success check:

- The sidebar is not dominated by old sessions.
- Each cwd initially shows at most 5 conversations, unless expanded.
- An older selected conversation still appears highlighted.

### 3. Message rendering structure

- [x] Replace the single plain `<pre>` message rendering with structured message partials/helpers.
- [x] Visually distinguish user, assistant, system/status, tool, and error output.
- [x] Keep raw text safely escaped by default in this step.
- [x] Add message metadata only where useful, without clutter.
- [x] Reconcile live message rendering with the same visual structure where practical.
- [x] Add tests for role-specific rendering/classes.
- [x] Restart server and report the test URL plus what to verify.

Success check:

- Conversations are easier to scan.
- User and assistant messages are visually distinct.
- Live assistant output still appears correctly.

### 4. Fix live message role rendering regression

- [x] Inspect live RPC event payloads for prompt, assistant response, status, and custom/session rename events.
- [x] Ensure optimistic prompt bubbles remain `user` only and are not reused for assistant output.
- [x] Render assistant live response text as `assistant`, not `user`.
- [x] Render `custom`/session-status events as status/system-style messages, not chat user messages.
- [x] Add focused tests or fixtures for live event role mapping where practical.
- [x] Restart server and report the test URL plus what to verify.

Success check:

- In a new clean web session, user prompts appear as blue/right `USER` bubbles.
- Assistant replies appear as assistant-styled messages with `ASSISTANT`, not `USER`.
- Session rename/custom status events do not look like user chat messages.

### 5. Compact expandable Pi/tool output

- [x] Detect Pi tool/thinking/status content in historical messages where possible.
- [x] Render noisy Pi output as compact cards by default.
- [x] Use expandable details for full tool inputs/results/output.
- [x] Show concise summaries such as `bash`, `read`, `edit`, or `thinking`.
- [x] Apply similar compact rendering to live RPC events.
- [x] Keep errors prominent and expanded enough to notice.
- [x] Add tests for collapsed tool/thinking rendering.
- [x] Restart server and report the test URL plus what to verify.

Success check:

- Tool and thinking noise no longer overwhelms the chat.
- Clicking/expanding reveals the full details when needed.
- Errors remain visible.

### 6. Markdown rendering for assistant messages

- [x] Add a markdown renderer dependency.
- [x] Add HTML sanitization for rendered markdown.
- [x] Render assistant markdown as HTML.
- [x] Keep user-provided content safe from raw HTML/script injection.
- [x] Style headings, lists, links, blockquotes, inline code, and code blocks.
- [x] Preserve readable plain-text fallback for unusual message parts.
- [x] Add tests for markdown rendering and HTML sanitization.
- [x] Restart server and report the test URL plus what to verify.

Success check:

- Assistant answers render lists, code blocks, and formatting nicely.
- Unsafe HTML is not executed or emitted unsanitized.
- Existing plain sessions still render cleanly.

### 7. Fix live compact message update duplication regression

Regression noticed after splitting mixed assistant thinking/tool content from visible assistant replies and adding live markdown rendering.

Observed behavior:

- During live streaming, compact `thinking` and tool cards can be appended repeatedly for each incremental `message_update`.
- This produces many near-duplicate cards such as `I find`, `I find it`, `I find it interesting`, etc.
- The duplication can also affect the visible assistant reply itself, not only compact/internal messages.
- Historical session JSONL does not appear to contain those duplicates; the duplication is produced by browser-side live rendering.
- The likely cause is that live rendering does not have a robust per-live-message/segment identity and can append instead of update when event shapes change, including for normal assistant text after the recent mixed-segment/live-markdown changes.

- [x] Reproduce with a live prompt that emits thinking/tool updates.
- [x] Inspect actual live RPC event shapes for `message_start`, `message_update`, and `message_end` with compact segments.
- [x] Track all live assistant segments, including normal visible reply text and compact assistant/tool segments, by stable message/segment identity where available.
- [x] Update existing compact thinking/tool cards and visible assistant reply bubbles in place on incremental `message_update` instead of appending duplicates.
- [x] Start a new compact card or assistant reply bubble only for a real new segment/message identity or `message_start` boundary.
- [x] Reset live segment tracking on `turn_end`, `agent_end`, new prompt submit, and session switch/page load.
- [x] Preserve mixed-message behavior: compact thinking/tool card plus separate visible markdown assistant reply.
- [x] Add focused tests for live compact update behavior where practical.
- [x] Restart server and report the test URL plus what to verify.

Success check:

- Live thinking/tool content updates in place while streaming.
- A single thinking/tool operation does not create many incremental duplicate cards.
- The visible assistant reply itself also updates in place and does not duplicate during streaming.
- Final assistant reply remains visible as markdown and is not hidden inside the compact thinking block.

### 8. TUI-like compact bash/tool rendering

Observed behavior:

- The web UI renders low-level assistant compact/tool segments literally, e.g. a compact `thinking + bash` card containing `[thinking]` and raw tool-call JSON, followed by one or more separate `tool result / bash` cards.
- The Pi CLI/TUI renders the same interaction more cleanly as a shell command line plus output, e.g. `$ git status --short && git log --oneline -1 (timeout 30s)`, with the output shown once.
- This makes the web transcript noisier and less consistent with the CLI, even after duplicate live updates were fixed.

- [x] Inspect historical and live message shapes for bash `toolCall` + matching `toolResult` pairs.
- [x] Render bash tool calls in a TUI-like compact form with the command and relevant timeout/options summarized.
- [x] Pair or visually group matching bash tool results with their command where practical.
- [x] Avoid showing placeholder `[thinking]` when no useful thinking text is present.
- [x] Avoid duplicate-looking standalone tool result cards when the output is already represented with its command.
- [x] Keep raw tool-call JSON/details available in an expandable area for debugging.
- [x] Preserve prominent rendering for errors and non-bash tools.
- [x] Apply the same behavior to live and historical rendering where practical.
- [x] Add focused tests for bash tool-call/result rendering.
- [x] Restart server and report the test URL plus what to verify.

Success check:

- Bash tool use in the web UI looks close to the Pi CLI/TUI: command line first, output once.
- Raw arguments/results remain available when expanded.
- Non-bash tools and errors remain understandable.

### 9. Composer and command UX polish

Note: the command discovery block is temporarily hidden in the UI so it does not block testing the chat experience; restore/redesign it in this step.

- [x] Send the prompt with `Enter`, and insert new lines with `Shift+Enter`.
- [x] Improve textarea sizing and focus behavior.
- [x] Keep abort close to the composer while Pi is running.
- [x] Make slash commands easier to discover without occupying too much vertical space.
- [x] Consider lightweight slash-command filtering/autocomplete.
- [x] Show clearer sending/running/done states.
- [x] Add tests for rendered command UI where practical.
- [x] Restart server and report the test URL plus what to verify.

Success check:

- Sending prompts feels fast from the keyboard with plain `Enter`.
- Commands are discoverable but not visually noisy.
- The running/abort state is clear.

### 10. Visual polish pass

- [x] Improve spacing, typography, colors, borders, and hover states.
- [x] Consider a dark Discord-like theme as the default.
- [x] Add clear selected-session and active/running indicators.
- [x] Add copy buttons for assistant messages or code blocks if simple.
- [x] Improve empty, pending-session, and no-session states.
- [x] Avoid large framework adoption unless the plain CSS becomes painful.
- [x] Run the full test suite.
- [x] Restart server and report the test URL plus what to verify.

Success check:

- The app feels comfortable enough for regular personal use.
- Important actions are easy to find.
- The UI remains simple and maintainable.

## Later ideas, not part of this pass

- Search sessions by cwd/name/message text.
- Pin or favorite sessions.
- Date separators in conversations.
- Syntax highlighting for code blocks.
- Replace polling with SSE.
- Reconcile from `get_messages` after each completed turn.
- One RPC process per open/live session if single-active-session becomes limiting.
- Attachment/image support.
