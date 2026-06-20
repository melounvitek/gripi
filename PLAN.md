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
- After each extraction is tested, refactor the newly extracted code for readability before moving to the next step.
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

- [x] Move `SafeMarkdownRenderer` to `lib/rendering/markdown_renderer.rb` or equivalent.
- [x] Keep `/markdown` behavior unchanged.
- [x] Keep existing markdown sanitization/highlighting tests green.
- [x] Refactor the extracted markdown renderer for readability, simplicity, and obviousness.
- [x] Run full suite.

### 2. Extract prompt parsing concepts

- [x] Add `Prompts::SlashCommand` or equivalent.
- [x] Move `/name`, `/rename`, `/compact`, `/fork`, `/tree`, `/clone`, and `/new` parsing there.
- [x] Keep `/prompt` response payloads unchanged.
- [x] Run prompt/slash command tests.
- [x] Refactor the extracted prompt parsing code for readability, simplicity, and obviousness.
- [x] Run full suite.

### 3. Extract uploaded image handling

- [x] Add `Prompts::UploadedImages` or equivalent.
- [x] Move Rack upload normalization, validation, size/type checks, and base64 conversion there.
- [x] Keep error messages and statuses unchanged.
- [x] Run prompt upload tests.
- [x] Refactor the extracted upload handling code for readability, simplicity, and obviousness.
- [x] Run full suite.

### 4. Extract pending RPC session lifecycle

- [x] Add `Rpc::PendingSessionRegistry` or equivalent.
- [x] Move mutex/hash pending cwd behavior there.
- [x] Preserve pending-session path, cwd, and remapping behavior.
- [x] Run pending-session/remap tests.
- [x] Refactor the extracted pending-session code for readability, simplicity, and obviousness.
- [x] Run full suite.

### 5. Extract new and branched session workflows

- [x] Add `Rpc::StartNewSession.call` or equivalent workflow object.
- [x] Add `Rpc::BranchSession.call` or equivalent workflow object.
- [x] Preserve new/fork/clone redirects and JSON payloads.
- [x] Preserve pending real-session remapping behavior.
- [x] Run new/fork/clone/session remap tests.
- [x] Refactor the extracted session workflow code for readability, simplicity, and obviousness.
- [x] Run full suite.

### 6. Extract command catalog

- [x] Add `Rpc::CommandCatalog` or equivalent.
- [x] Move builtin command list and RPC command filtering there.
- [x] Preserve `/commands` behavior, including hiding internal `pi_web_tree` commands.
- [x] Run command tests.
- [x] Refactor the extracted command catalog code for readability, simplicity, and obviousness.
- [x] Run full suite.

### 7. Extract session view preparation

- [x] Add `Sessions::SessionView` or equivalent read model/builder.
- [x] Move `prepare_session_view` data assembly there.
- [x] Initially keep existing instance variable names for ERB compatibility.
- [x] Preserve page, sidebar, modal, and session fragment behavior.
- [x] Run page/sidebar/session fragment tests.
- [x] Refactor the extracted session view code for readability, simplicity, and obviousness.
- [x] Run full suite.

### 8. Extract sidebar read model

- [x] Add `Sessions::Sidebar` or equivalent.
- [x] Move sidebar sorting, unread grouping, search, project filtering, pagination, and load-more rules there.
- [x] Keep ERB output unchanged.
- [x] Run sidebar tests.
- [x] Refactor the extracted sidebar code for readability, simplicity, and obviousness.
- [x] Run full suite.

### 9. Extract session family read model

- [ ] Add `Sessions::SessionFamily` or equivalent.
- [ ] Move parent/child/root indexing rules there.
- [ ] Run relation/tree/sidebar tests.
- [ ] Refactor the extracted session family code for readability, simplicity, and obviousness.
- [ ] Run full suite.

### 10. Review remaining `app.rb` helpers and stop intentionally

- [ ] Review remaining private helpers.
- [ ] Extract more only if a clear business concept remains.
- [ ] Leave small Sinatra/view helpers in place when extracting them would create noise.
- [ ] Run full suite.

### 11. Finish the plan

- [ ] Move completed `PLAN.md` into `plans/`.
