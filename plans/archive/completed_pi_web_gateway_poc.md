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

After each implementation step, restart the local Sinatra/Puma server so route changes are loaded, then verify the current app on:

> http://100.x.y.z:4567/

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

- [x] Selecting a session starts/attaches RPC.
- [x] Sending a browser prompt goes through Pi.
- [x] The native session file is updated by Pi.
- [x] `get_messages` returns the new conversation state.

### 3. Live output rendering

- [x] Add SSE or polling for live updates.
- [x] Capture and buffer RPC events from the active process.
- [x] Render basic Pi-like output:
  - [x] user messages
  - [x] assistant text deltas
  - [x] thinking deltas, if present
  - [x] tool execution start/update/end
  - [x] errors
  - [x] queued/running state

Success check:

- [x] Ask Pi to inspect files or run a command.
- [x] Browser shows useful live tool/progress activity.
- [x] Final assistant answer appears without refreshing manually.

### 4. Native session creation and switching

- [x] Add “New session” action for a selected cwd.
- [x] Create the session through Pi-native behavior.
- [x] Add session switching.
- [x] Prefer RPC `switch_session`; restarting the active `pi --mode rpc --session <file>` process is acceptable for PoC.
- [x] Refresh sidebar after session creation/switching.
- [x] Keep a pending new session selected before Pi persists its JSONL file on first assistant response.
- [x] Ignore stale event polls from old browser pages so they do not steal the one active RPC process.

Success check:

- [x] New session appears as a native Pi JSONL file.
- [x] Prompting in the new session works.
- [x] Switching back to an older session shows its messages.

### 5. Slash command discovery

- [x] On session open, call RPC `get_commands`.
- [x] Show command suggestions for:
  - [x] extension commands
  - [x] prompt templates
  - [x] skills
- [x] Submitting `/command` sends it via RPC `prompt`.
- [x] Add simple RPC-backed buttons for built-ins:
  - [x] new session
  - [x] abort
  - [x] compact
  - [x] rename

Success check:

- [x] Installed user commands/skills appear in browser suggestions.
- [x] Running an extension/prompt/skill command works.
- [x] Built-in limitations are understood and documented.

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
