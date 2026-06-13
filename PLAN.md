# Pi Web Gateway PoC Plan

## Goal

Build a small Ruby/Sinatra/HTMX proof of concept for a local browser UI over Pi.

The gateway should stay thin:

- Pi owns session history.
- Pi owns prompt execution, tools, compaction, model state, branching, and commands.
- The web app owns only the browser UI and Pi process orchestration.
- No gateway DB for conversation history.

Target experience:

> Run the web gateway on a machine where Pi is configured, open localhost, see Pi sessions grouped by working directory, switch/create sessions, and talk to Pi in the browser.

## Step delivery check

After each implementation step, verify the current app on:

> http://100.103.198.74:4567/

Then report the exact link to open and briefly describe what to test there.

## Key Pi facts to preserve

- Native sessions are JSONL files under `~/.pi/agent/sessions/--encoded-cwd--/*.jsonl`.
- Session files include `cwd`, session id, messages, tree/branch entries, labels, compaction, model/thinking changes, and `session_info` names.
- Pi SDK has `SessionManager.listAll()` / `SessionManager.list(cwd)` with useful metadata, but the PoC can parse JSONL directly first.
- Ruby can drive Pi through:

```bash
pi --mode rpc --session <session-file>
```

- Important RPC commands:
  - `get_state`
  - `get_messages`
  - `prompt`
  - `abort`
  - `new_session`
  - `switch_session`
  - `fork`
  - `clone`
  - `compact`
  - `get_session_stats`
  - `set_session_name`
  - `get_available_models`
  - `set_model`
  - `set_thinking_level`
  - `get_commands`
- RPC streams live events for assistant deltas, thinking deltas, tool calls/results, queue updates, compaction, retries, and errors.
- `get_commands` returns extension commands, prompt templates, and skills.
- Built-in TUI commands are not returned by `get_commands`; map important ones to RPC-backed web buttons instead.

## PoC checklist

### 1. Native session browser

- [x] Create minimal Ruby/Sinatra app.
- [x] Scan `~/.pi/agent/sessions/**/*.jsonl`.
- [x] Parse session metadata:
  - [x] file path
  - [x] cwd
  - [x] session id
  - [x] display name from latest `session_info`
  - [x] first user message fallback
  - [x] message count
  - [x] created/modified timestamps
- [x] Render sessions grouped by cwd.
- [x] Let user click a session.
- [x] Render read-only messages from the selected JSONL file.

Success check:

- [x] Browser shows real Pi sessions grouped by directory.
- [x] A known session opens with recognizable history.
- [x] No DB or copied conversation state exists.

### 2. Minimal Pi RPC client

- [x] Add Ruby `PiRpcClient`.
- [x] Spawn `pi --mode rpc --session <session-file>`.
- [x] Send JSONL commands to stdin.
- [x] Read JSONL responses/events from stdout.
- [x] Implement request/response correlation by `id`.
- [x] Support:
  - [x] `get_state`
  - [x] `get_messages`
  - [x] `prompt`
  - [x] `abort`
- [x] Use one active Pi RPC process at a time for the PoC.

Success check:

- [ ] Selecting a session starts/attaches RPC.
- [ ] Sending a browser prompt goes through Pi.
- [ ] The native session file is updated by Pi.
- [ ] `get_messages` returns the new conversation state.

### 3. Live output rendering

- [ ] Add SSE or polling for live updates.
- [ ] Capture and buffer RPC events from the active process.
- [ ] Render basic Pi-like output:
  - [ ] user messages
  - [ ] assistant text deltas
  - [ ] thinking deltas, if present
  - [ ] tool execution start/update/end
  - [ ] errors
  - [ ] queued/running state

Success check:

- [ ] Ask Pi to inspect files or run a command.
- [ ] Browser shows useful live tool/progress activity.
- [ ] Final assistant answer appears without refreshing manually.

### 4. Native session creation and switching

- [ ] Add “New session” action for a selected cwd.
- [ ] Create the session through Pi-native behavior.
- [ ] Add session switching.
- [ ] Prefer RPC `switch_session`; restarting the active `pi --mode rpc --session <file>` process is acceptable for PoC.
- [ ] Refresh sidebar after session creation/switching.

Success check:

- [ ] New session appears as a native Pi JSONL file.
- [ ] Prompting in the new session works.
- [ ] Switching back to an older session shows its messages.

### 5. Slash command discovery

- [ ] On session open, call RPC `get_commands`.
- [ ] Show command suggestions for:
  - [ ] extension commands
  - [ ] prompt templates
  - [ ] skills
- [ ] Submitting `/command` sends it via RPC `prompt`.
- [ ] Add simple RPC-backed buttons for built-ins:
  - [ ] new session
  - [ ] abort
  - [ ] compact
  - [ ] rename

Success check:

- [ ] Installed user commands/skills appear in browser suggestions.
- [ ] Running an extension/prompt/skill command works.
- [ ] Built-in limitations are understood and documented.

## Non-goals for this PoC

- [ ] No DB.
- [ ] No auth.
- [ ] No multi-user support.
- [ ] No production packaging.
- [ ] No exact Pi terminal renderer.
- [ ] No full `/settings` parity.
- [ ] No attachments/images initially.
- [ ] No complex model picker unless needed.

## Open questions to answer during PoC

- [ ] Is direct JSONL parsing good enough for the session sidebar?
- [ ] Is `pi --mode rpc --session <file>` stable enough for a long-running local web app?
- [ ] Are RPC events rich enough for a useful Pi-like browser UI?
- [ ] Is `get_commands` plus RPC-backed web controls enough for commands?
- [ ] Is one active Pi process enough, or do we need one process per open session?
- [ ] Does Ruby/Sinatra/HTMX still feel good once live streaming is added?

## Useful RPC snippets

```json
{"id":"state-1","type":"get_state"}
{"id":"messages-1","type":"get_messages"}
{"id":"prompt-1","type":"prompt","message":"Hello"}
{"id":"abort-1","type":"abort"}
{"id":"switch-1","type":"switch_session","sessionPath":"/path/to/session.jsonl"}
{"id":"new-1","type":"new_session"}
{"id":"commands-1","type":"get_commands"}
{"id":"compact-1","type":"compact","customInstructions":"Focus on recent implementation decisions"}
{"id":"name-1","type":"set_session_name","name":"my task"}
```
