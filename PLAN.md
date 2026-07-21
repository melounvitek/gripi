# Reduce misleading RAM reporting and session metadata read amplification

Status: awaiting implementation approval

## Goal

Make the resource indicator distinguish the service working set from reclaimable cgroup file cache, and stop sidebar polling from repeatedly rescanning growing session files while gateway-managed Pi sessions are busy.

This plan deliberately keeps the Ruby application and the system allocator. It adds no jemalloc dependency and does not change Pi-owned session files or native Pi workflows.

## Findings and baseline

### Live service

- Raw `gripi.service` cgroup memory was about 8.6 GiB.
- About 6.3 GiB was `inactive_file`, so the Kubernetes-style approximate working set (`memory.current - inactive_file`) was about 2.3–2.4 GiB.
- The host had about 20 GiB available, with no current cgroup memory pressure or OOM events.
- Puma RSS grew from about 1.55 GiB at the initial inspection to about 1.70 GiB. Most was private anonymous memory.
- The two Pi processes together used about 335 MiB RSS.
- Puma had logically read about 100 GiB in roughly 3.5 hours.
- A 30-second observation while the UI was otherwise idle recorded 250 MiB of additional Puma reads, in regular 2.5-second steps, and about 3 MiB of RSS growth.

The read cadence matches `SidebarController#refreshDelay`: active sessions refresh `/sidebar` every 2.5 seconds. Each request builds all session metadata. The class cache invalidates whenever an active JSONL's size or mtime changes, and `PiSessionStore#session_from_metadata` then scans that whole file.

The corpus contains a 19 MiB session with a 7.8 MiB JSONL record. `File.foreach` materializes that record even though the metadata fast path avoids parsing canonical tool results.

### Isolated A/B tests

The benchmark used eight threads and copies of the real 19 MiB session. Each thread appended 40 small native-shaped metadata entries.

| Variant | Logical reads | Peak RSS | Workload time |
|---|---:|---:|---:|
| Current full refresh after every append, glibc default | 6.4 GiB | 192 MiB | 60 s |
| Defer while busy, then one normal refresh, glibc default | 457 MiB | 55 MiB | 0.7 s |

The deferred result includes copying, initial scans, and final scans. After warm-up, it reduced rescan reads by about 40x and peak RSS by about 72% in this workload.

Allocator experiments did not justify a deployment change:

- `MALLOC_ARENA_MAX=2` made the metadata workload slower and increased peak RSS, although it helped a separate large-JSON parsing workload.
- jemalloc produced higher peaks in the metadata workload; aggressive decay also substantially increased runtime.
- `malloc_trim(0)` proved that glibc retained some free pages, but periodically calling it would be Linux/glibc-specific and would mask the allocation source.

Decision: retain default system allocation. Do not add jemalloc, force allocator tuning, or call `malloc_trim` from the application.

## Intended behavior and tradeoff

While a gateway-managed session is busy, compacting, or still reporting an active agent run:

- `/sidebar` reuses the last successfully parsed, non-`nil` metadata for that session even if its file grows.
- Running/compacting indicators remain current because `PiRpcClient#busy?` covers agent runs, compaction, and active bash work.
- The session title, ordering, assistant-response count, and unread state may remain briefly stale while Pi is working.
- As soon as the RPC client settles, the next sidebar request performs one normal exact metadata refresh.
- A session without successfully parsed cached metadata is still scanned, so newly created or initially incomplete sessions become visible while busy.
- If settlement races with rendering after metadata was deferred, the response carries a nonvisual deferred marker that keeps the next refresh at 2.5 seconds instead of allowing the settled 10-second interval.

Full conversation loads, the new-session modal, and non-sidebar store users retain current exact-refresh behavior. Sessions being changed by native Pi CLI processes also retain current behavior because they are not marked busy by the gateway RPC registry.

This bounded staleness is preferred over an incremental metadata parser. A truly exact incremental parser would either have to reread/hash the old prefix on every append (preserving the current I/O problem) or assume that same-inode growth is append-only (weakening existing rewrite detection). The proposed approach leaves the durable Pi format and its rewrite handling unchanged.

## TDD rounds

### Round 1 — report working-set memory

Tests first:

1. Extend `test/resource_usage_monitor_test.rb` with representative `memory.stat` data and assert:
   - raw cgroup memory remains available;
   - inactive file bytes are exposed;
   - approximate working set is `max(memory.current - inactive_file, 0)`;
   - malformed or missing required cgroup data preserves the existing unsupported behavior.
2. Extend `test/resource_usage_routes_test.rb` to cover the new API fields while retaining `memoryBytes` for diagnostics/backward compatibility.
3. Update `test/resource_usage_controller_js_test.rb` to specify the visible working-set value, reclaimable cache, Puma RSS, and Pi RSS.

Implementation:

- Parse `memory.stat` in `ResourceUsageMonitor`.
- Add `working_set_bytes` and `inactive_file_bytes` to the snapshot.
- Expose them as `workingSetBytes` and `inactiveFileBytes`; retain raw `memoryBytes`.
- Render the primary value as `RAM 2.4 GB approx. working set` or similarly precise wording.
- Show `inactive file cache` separately in the breakdown or tooltip so the raw total remains explainable without describing all of it as unconditionally reclaimable.
- Keep wording explicit that process RSS values do not have to sum to the cgroup working set.

Verification:

- Run focused Ruby and JavaScript tests.
- Commit the completed round independently.

### Round 2 — defer busy-session sidebar metadata refreshes

Tests first:

1. Add a `PiSessionStore` regression test proving that:
   - a successfully cached session remains available while refresh is deferred despite appended data;
   - the first read is never deferred when no cache entry exists;
   - an unsuccessful cached parse followed by an appended session header is reparsed even while deferral is requested;
   - disabling deferral causes the next read to return all appended metadata;
   - ordinary stores continue to refresh immediately;
   - a rewrite-and-append is read exactly after settlement and the deferred read does not advance the cached signature.
2. Add a request-level `/sidebar` regression test proving that repeated requests reuse metadata while the registry reports the session busy, then observe the final response count and unread state after settlement.
3. Add a deterministic settlement-race test: if metadata was deferred but `busy?` clears before indicator rendering, the rendered response still requests one more 2.5-second refresh.
4. Preserve existing concurrent cache-miss and conversation-index rewrite/append tests.

Implementation:

- Give `PiSessionStore` an optional behavioral predicate for deferring refresh only when the cache contains a successfully parsed, non-`nil` session.
- Record whether a store build actually deferred metadata without advancing its cached file signature.
- Keep class-cache locking and limits unchanged.
- Add an explicit `defer_busy_metadata:` intent used only by the `/sidebar` route; do not infer it from `include_conversation` because the new-session modal is also a non-conversation view.
- Use only `PiRpcClientRegistry#busy?` for the decision; it already covers agent runs, compaction, and active bash work and avoids redundant non-atomic state reads.
- If a sidebar response contains deferred metadata, render a nonvisual marker that `SidebarController#refreshDelay` treats like an active indicator. This closes the settlement/render race while adding at most one extra 2.5-second poll.
- Do not apply deferral to full conversation/session-fragment loads, the new-session modal, or unrelated store callers.

Verification:

- Run focused store, session-view, and request tests.
- Rerun the real-session benchmark. Acceptance targets for the eight-thread/40-append workload:
  - no more than 500 MiB logical reads;
  - no more than 80 MiB peak RSS;
  - final metadata exactly matches the file after settlement.
- Commit the completed round independently.

### Round 3 — integrated verification and review

1. Run `mise run test`.
2. Run relevant managed Chromium E2E coverage for sidebar live status and resource reporting.
3. Launch only an isolated alternate-port app if browser presentation needs manual verification; do not restart `gripi.service` without explicit approval.
4. Perform a final independent subagent review focused on simplification, cache races (including settlement during rendering), stale-state behavior, native Pi compatibility, and code philosophy.
5. Apply actionable simplifications and rerun affected tests/review when changes are substantive.
6. After explicit restart approval, establish a clean production baseline and observe an active session:
   - the old 2.5-second multi-megabyte read steps should disappear while the gateway session is busy;
   - working-set and inactive-cache values should match `memory.stat` within sampling races;
   - Puma RSS should be evaluated from the clean restart rather than expecting glibc to return all existing high-water allocations.
7. Move this completed plan to `plans/reduce_memory_read_amplification.md`.

## Out of scope / follow-up only if evidence remains

- Rewriting the gateway in Go or Rust.
- Bundling or requiring jemalloc.
- Periodic GC, compaction, or `malloc_trim` jobs.
- Changing Pi's JSONL format or replacing native append workflows.
- Building a persistent/incremental metadata index.
- Broad RPC-state caps or registry lifetime changes without measurements showing they materially contribute after the rescan fix.

If Puma anonymous memory still grows materially from a clean restart after Round 2, the next investigation should instrument live Ruby object size, GC statistics, RPC payload/replay sizes, and client counts before changing allocator or language.
