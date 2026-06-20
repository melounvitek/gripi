# Backend Refactor Plan

## Goal

Preserve current behavior exactly while extracting clear backend concepts from `app.rb` into `lib/`.

## Non-goals

- Do not redesign frontend behavior.
- Do not intentionally change rendered HTML, JavaScript, or CSS.
- Do not replace the current test suite before the refactor.
- Do not introduce abstraction-only one-line wrappers.
- Do not create generic `services/` or junk-drawer folders.

## Refactor rules

- Keep each step behavior-preserving.
- Run the full test suite after each step: `bundle exec ruby -Itest test/*_test.rb`.
- Prefer durable business concepts over mechanical extraction.
- Use service objects only for workflows/actions; service class names start with a verb and expose `.call`.
- Keep Sinatra-specific response handling in `app.rb` unless a later step clearly justifies changing that.
- Keep diffs small enough to review.
- If an extraction starts producing many tiny pass-through methods, stop and reconsider the shape.

## Baseline

- Current suite command: `bundle exec ruby -Itest test/*_test.rb`
- Last observed result before this plan: 193 runs, 2063 assertions, 0 failures.

## Steps

### 1. Extract markdown rendering

- [ ] Move `SafeMarkdownRenderer` to `lib/rendering/markdown_renderer.rb` or equivalent.
- [ ] Keep `/markdown` behavior unchanged.
- [ ] Keep existing markdown sanitization/highlighting tests green.
- [ ] Run full suite.

### 2. Extract prompt parsing concepts

- [ ] Add `Prompts::SlashCommand` or equivalent.
- [ ] Move `/name`, `/rename`, `/compact`, `/fork`, `/tree`, `/clone`, and `/new` parsing there.
- [ ] Keep `/prompt` response payloads unchanged.
- [ ] Run prompt/slash command tests.
- [ ] Run full suite.

### 3. Extract uploaded image handling

- [ ] Add `Prompts::UploadedImages` or equivalent.
- [ ] Move Rack upload normalization, validation, size/type checks, and base64 conversion there.
- [ ] Keep error messages and statuses unchanged.
- [ ] Run prompt upload tests.
- [ ] Run full suite.

### 4. Extract pending RPC session lifecycle

- [ ] Add `Rpc::PendingSessionRegistry` or equivalent.
- [ ] Move mutex/hash pending cwd behavior there.
- [ ] Preserve pending-session path, cwd, and remapping behavior.
- [ ] Run pending-session/remap tests.
- [ ] Run full suite.

### 5. Extract new and branched session workflows

- [ ] Add `Rpc::StartNewSession.call` or equivalent workflow object.
- [ ] Add `Rpc::BranchSession.call` or equivalent workflow object.
- [ ] Preserve new/fork/clone redirects and JSON payloads.
- [ ] Preserve pending real-session remapping behavior.
- [ ] Run new/fork/clone/session remap tests.
- [ ] Run full suite.

### 6. Extract command catalog

- [ ] Add `Rpc::CommandCatalog` or equivalent.
- [ ] Move builtin command list and RPC command filtering there.
- [ ] Preserve `/commands` behavior, including hiding internal `pi_web_tree` commands.
- [ ] Run command tests.
- [ ] Run full suite.

### 7. Extract session view preparation

- [ ] Add `Sessions::SessionView` or equivalent read model/builder.
- [ ] Move `prepare_session_view` data assembly there.
- [ ] Initially keep existing instance variable names for ERB compatibility.
- [ ] Preserve page, sidebar, modal, and session fragment behavior.
- [ ] Run page/sidebar/session fragment tests.
- [ ] Run full suite.

### 8. Extract sidebar read model

- [ ] Add `Sessions::Sidebar` or equivalent.
- [ ] Move sidebar sorting, unread grouping, search, project filtering, pagination, and load-more rules there.
- [ ] Keep ERB output unchanged.
- [ ] Run sidebar tests.
- [ ] Run full suite.

### 9. Extract session family read model

- [ ] Add `Sessions::SessionFamily` or equivalent.
- [ ] Move parent/child/root indexing rules there.
- [ ] Run relation/tree/sidebar tests.
- [ ] Run full suite.

### 10. Review remaining `app.rb` helpers and stop intentionally

- [ ] Review remaining private helpers.
- [ ] Extract more only if a clear business concept remains.
- [ ] Leave small Sinatra/view helpers in place when extracting them would create noise.
- [ ] Run full suite.
- [ ] Move completed `PLAN.md` into `plans/`.
