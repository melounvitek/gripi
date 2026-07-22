# Go gateway rewrite

The Ruby gateway remains the production implementation until the final cutover. Each completed round is committed independently.

## Goal

Replace Sinatra/Puma with a Go gateway while preserving native Pi data and workflows, browser behavior, security boundaries, and operational behavior. Under the same warmed fixture workload, the Go gateway process should use at most 40% of the Ruby gateway RSS without materially worsening request latency.

The browser JavaScript, Electron shell, CSS, and Pi TypeScript extension remain in their native languages.

## Rounds

- [x] 1. Establish the Ruby memory baseline, Go toolchain, configuration, embedded assets, process lifecycle, and migration test commands.
- [x] 2. Port HTTP security, browser approval, secure state stores, PWA endpoints, and Go templates.
- [x] 3. Port session discovery/indexing, history rendering, attachments, Markdown, sidebar, search, and pagination.
- [ ] 4. Port Pi RPC subprocess management, event buffering, registry concurrency, idle retirement, and session synchronization.
- [ ] 5. Port prompts, bash, streaming controls, model settings, trees, branches, extension UI, and remaining session actions.
- [ ] 6. Port multi-user ownership, resource monitoring, updates/restarts, setup tooling, and operational documentation.
- [ ] 7. Pass all contract suites, benchmark the warmed implementation, run a real-Pi smoke test, remove Ruby/Puma, and complete independent review.

Delete this file when the final round is complete.
