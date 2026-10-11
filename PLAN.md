# PLAN: Pi behind an agent adapter

Goal: invert the dependencies so Gripi's core and server talk to a small agent-neutral contract, and Pi is one adapter behind it. **Nothing changes for users**, and only Pi is supported. Later work adds other agents (Hermes, Claude, Codex) through a generic ACP adapter, so the contract must not be Pi-shaped. It has been checked on paper against ACP.

Investigation reports (read the parts relevant to your step):
- `/home/vitek/Work/.gripi-agent-adapters/reports/A-backend.md`: backend Pi coupling, step details, test inventory, risks.
- `/home/vitek/Work/.gripi-agent-adapters/reports/B-browser.md`: Pi formats in the browser, live-path tests and gaps.
- `/home/vitek/Work/.gripi-agent-adapters/reports/C-contract.md`: contract sketch checked against Hermes ACP and Claude ACP.

## Target layout

```
cmd/gripi             wiring: builds the Pi adapter from config, passes it to the server
internal/server   ──► internal/agent        core: neutral types, small interfaces, generic session machinery
internal/agent/pi ──► internal/agent        Pi adapter: composer, store, client wrapper; subpackages rpc, settings, sessionfile
internal/sessions     gateway-owned session data only (tags, pins, read state, completions, attachments, projects, suggestions)
```

Import rules, enforced by an architecture test on production imports. Test files may still build real Pi clients through helpers.
- Only `cmd/gripi` imports `internal/agent/pi`.
- `internal/agent` imports no adapter, `internal/server` or `internal/sessions`.
- `internal/server`, `internal/sessions` and `internal/access` import neither the adapter nor the old `internal/rpc` / `internal/pi`.

The test starts with an allow-list of today's violations. The list belongs to the coordinator: workers don't edit it, and stale entries don't fail the test. The coordinator shrinks it after each merge, and it must be empty at the end.

## Decisions

1. **Session key.** Keep today's value (the absolute Pi JSONL path), but the core and server treat it as an opaque string. Path semantics live behind the adapter: exists, aliases/canonical, valid key, delete, pending-key generation. No state migration; URLs, gateway state files, attachments and the `gripi` CLI output stay the same.
2. **Generic machinery belongs to the core**, so `internal/agent` holds more than types: the client registry and lanes, pending sessions, maintenance, diagnostics, the event-batch cursor contract, and start/branch orchestration with adapter hooks.
3. **Browser formats stay byte-identical in this PR.** This covers `/events` items, the live snapshot `data-*` attributes, model settings, fork messages, tree entries and the bash prompt response. Where the browser still needs Pi-shaped data, the adapter supplies it as opaque JSON that the core and server pass through without reading fields. Every field the *server* reads today becomes typed. A follow-up PR replaces the browser formats with a Gripi event model (B's proposal). Out of scope here.
4. **Contract shape.** Follow C's naming where it costs nothing, but build only what the server needs today:
   - `Delivery` Start/Steer/FollowUp
   - `Disposition`
   - `Cancelled{Restored, Forced}`
   - typed `Status`/`ContextUsage`
   - `Command{Name, Description, Group}`
   - typed state and branch results
   - Pi-only features as small optional interfaces, each with a `var _` compile-time assertion against the real adapter type

   No ACP, permission prompts, history cursors or setting categories yet.
5. **Moved types keep their field names and JSON tags.** Templates fail silently, and `session-baselines.json` and the metadata cache serialize by field name. User-visible strings stay too, including the "Pi RPC …" errors.
6. **Temporary aliases are allowed** while steps are in flight, so callers move gradually. Remove them by the final step.
7. `pi_extensions/gripi-tree.ts` stays where it is (`embed.go`, npm script). `cmd/gripi` hands it to the adapter.
8. Idiomatic Go: a few small interfaces and typed structs, no layers of ceremony, no speculative features.
9. **Don't bake in Pi's process model.** The contract must not make an ACP adapter rework it later:
   - A core `Client` is one *session*, not necessarily a process, and `Close` releases that session.
   - Branching (new/fork/clone) returns a result that says whether the same client now serves another session (`Moved`, as with Pi). The neutral orchestration must not assume it always does; ACP forks create a separate session. For now it fails explicitly on `Moved=false` (see below).
   - Known ACP prerequisites, deliberately left for the ACP PR because no current adapter exercises them: branches that create a session the client does not serve (`Moved=false`) fail explicitly instead of opening the new session, and `Cancel` doesn't yet report the prompts it dropped (`Cancelled{Restored, Forced}`). Pi's restore stays in the registry's `QueueRestorer`.
   - Submitting a prompt returns once the adapter has accepted it and never waits for the turn to end. ACP has no Pi-style early acknowledgement, so acceptance means the adapter took the prompt, not necessarily that the agent confirmed it.

## Steps

Each step leaves the app behaving identically, with `mise run test` and `go test -race ./...` green, and is committed separately. Run e2e at the marked milestones, serialized with `flock /tmp/gripi-agent-adapters-e2e.lock mise run e2e`. Details are in report A §6 unless noted.

### Round 1 (four workers in parallel)

- **S0, guard rails.**
  - The architecture import test with the allow-list (production imports only).
  - Tests for the untested prompt branches (A §7.2 items 1–3): the deferring-compaction guard in `CloseClientIfStartedBefore`, internal-command rejection, images with a bash command, `/name` without an argument, an extension command with images or during compaction, and `pendingIdentificationError`.
  - New test files only, and none depending on private helpers that S1a moves.
- **S1a, core machinery.**
  - Move into `internal/agent`: the registry, pending sessions, maintenance, diagnostics, the client interface, `EventBatch`, `LiveSnapshot`, `PromptImage`, errors and `StopResult`. Aliases go in `internal/rpc`. S1a owns `internal/agent/doc.go`.
  - Resolve its own compile traps:
    - `rpc/session_workflows.go` uses private registry members (`registerStartedAt`, the clock). Export minimal methods or move the workflow, and record which.
    - `SessionPosition`/`SessionEntriesAfter` reference the Pi-only `SessionEntries`. Split them out of the core client interface into a Pi-local interface used by the synchronizer.
    - Move or duplicate the shared test fakes (`registryClient`).
  - Add named optional interfaces plus `var _` assertions for `DeferringCompactionPrompts`, `QueuedMessagesForStop` and `ClearQueue`.
- **S1b, display types.** Move `Session`, `Image`, `Message`, `Status`, `Window`, `ToolCallContext`, `DisplayHomePath` and `NotificationPreview` into `internal/agent`. Aliases go in `internal/sessions`.
- **T, characterization tests for the live path** (report B §5.4: P1 items 2–5, P2 items 7–10).
  - Prefer fake Pi scenarios plus e2e assertions; use Node tests only for renderer details.
  - Pin current behaviour, quirks included, and don't change fake Pi's existing message format.
  - T owns `e2e/`, `test/*.mjs` and fake Pi.

### Round 2
S2 (rewrite imports to the core) plus S3:
- the adapter root `internal/agent/pi`;
- wiring moved from `server.go` to `cmd/gripi`;
- `NewHandler` takes the adapter;
- a shared test helper for the 68 `NewHandler` calls.

**e2e.**

### Round 3
- **S4:** the opaque key and the `Store` interface, with aliasing injected into the gateway stores and `access`.
- **S5:** the `Guard` interface over the synchronizer.

### Round 4 (S6, then S7)
They run one after the other because they share `rpc/client.go`, `local_routes.go`, `completion_notifications.go` and the contract files.
- **S6:**
  - typed results for every server read of Pi payloads;
  - `ActionClient` split into core actions plus optional Pi interfaces;
  - the command catalog, slash/bash parsing, thinking levels and tree filters moved to the adapter as typed metadata;
  - start/branch using typed state.
- **S7:** completion notifications and live output consume typed lifecycle signals from the adapter instead of raw Pi events.

Optional interfaces are asserted on the client the adapter factory actually returns, not only on the concrete type. **e2e.** Sol review.

### Rounds 5 and 6 (one worker)
Move the Pi code into subpackages of the adapter instead of merging namespaces, which avoids name collisions:
- `internal/rpc` → `internal/agent/pi/rpc`;
- `internal/pi` → `internal/agent/pi/settings`;
- the Pi JSONL reader and synchronizer → `internal/agent/pi/sessionfile`. `internal/sessions` keeps only gateway-owned data.

Then remove the aliases and the allow-list, and document the layers. **e2e.**

### Round 7: final verification
- Full `mise run test`, `go test -race ./...`, `go vet ./...` and e2e.
- A deterministic behaviour comparison against `master`. Run the same fake Pi scenarios and sessions on both builds and compare rendered HTML, `/events` payloads, HTTP status and error bodies, and `gripi` CLI JSON.
- An upgrade check: the new build loads gateway state, `session-baselines.json` and the metadata cache written by `master`. No session flips to external follow, and no pins, tags or read state are lost.
- Shutdown order, with baselines saved after Pi stops.
- Notification identity and read suppression unchanged (`completedReplyID`).
- A manual Chromium check with screenshots.
- Sol review of the whole branch.

## Rules for workers
- Work on your own branch `chore/agent_adapters_<step>`, off `chore/agent_adapters`, in `~/Work/.worktrees/gripi/<branch>/`. Commit there; the coordinator merges.
- Existing behaviour tests (e2e specs, `test/*.mjs`) may change only mechanically (imports, type names, constructor helpers). A failing behaviour test is a regression to fix, not a test to adjust. New characterization tests that pin current behaviour are welcome.
- Don't edit `PLAN.md` or files owned by a parallel step. Don't add scope. If something in the plan turns out wrong, stop and report.
- The coordinator runs the Sol reviews at milestones; workers skip the independent review loop.
