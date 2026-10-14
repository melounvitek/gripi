# Backend architecture

The gateway runs coding agents through a small contract in `internal/agent`, and Pi is one adapter behind it. Hermes Agent is a second, experimental one, which only `GRIPI_HERMES` turns on. Parts of the contract still follow Pi (see [Pi-shaped parts](#pi-shaped-parts)).

## Layers

- `cmd/gripi` builds the Pi adapter from the configuration and passes it to the server. With `GRIPI_HERMES`, it passes a `Router` instead, which serves Pi and Hermes as one adapter.
- `internal/server` serves the browser, the local API and the `gripi` commands. It reaches the agent only through `internal/agent`.
- `internal/agent` is the core: neutral types, the contract, and the machinery that runs session clients (the client registry, pending sessions, maintenance, diagnostics, starting and branching sessions). It imports no adapter and no gateway package.
- `internal/agent/hermes` is the Hermes adapter. It runs every Hermes session in one `hermes acp` process and speaks ACP, the Agent Client Protocol, to it.
- `internal/agent/pi` is the Pi adapter. Its subpackages are `rpc` (Pi's RPC client and process), `settings` (Pi's settings files) and `sessionfile` (the reader of Pi's JSONL session files, and the synchronizer that guards them against Pi CLI).
- `internal/sessions` keeps what the gateway owns about sessions: read state, pins, tags, completions, projects, attachments and path suggestions.

Only `cmd/gripi` and an adapter itself import that adapter's package. `internal/architecture_test.go` checks these rules for production code; tests may still build real Pi clients.

## Adapters

An adapter implements `agent.Adapter`, which starts a `Client` for each session and returns the adapter's `Store`, `Guard` and `Composer`:

- A `Client` serves one session, which need not be one process.
- The `Store` lists and reads saved sessions. Its keys are opaque to the gateway; Pi's are the absolute paths of its session files.
- The `Guard` keeps the gateway from writing a session that someone else writes, such as Pi CLI. An agent whose sessions nobody else writes can pass every call through.
- The `Composer` says how the agent reads what the user types, such as Pi's `!` and `/` commands.

A `Router` sends each session to the agent whose store accepts its key: Pi's keys are paths, Hermes's `hermes:<session ID>`. It names each session's agent in `Session.Agent`, which keeps Hermes's sessions out of the session list, and starts a new session with the agent that `StartOptions.Agent` names. The server keeps one current Hermes conversation, which the Hermes panel shows. Without Hermes, the gateway gets the Pi adapter itself, and so works as it did before.

Features that only some agents have, such as the prompt queue, shell commands, tree navigation, model selection and extension dialogs, are optional interfaces in `internal/agent/capabilities.go`. The server checks for them with type assertions, and the Pi adapter asserts at compile time that its client implements them.

## Pi-shaped parts

- The browser still reads Pi's formats: live events and snapshots, model settings, fork messages and session trees. The adapter returns them as opaque JSON that the core and server pass through without reading; only what the server itself reads is typed. Server-rendered history uses the core's `Message`, whose vocabulary is still Pi's. The Hermes adapter therefore turns ACP's updates into Pi's live events, and projects the same updates onto `Message`s for the history.
- Some core error texts and the diagnostics component still name Pi, because users and logs see them.
- Every `Client` must switch its session in place for a new session (`NewSession`), and a branch whose client keeps serving the old session fails on purpose. `Cancel` doesn't report the prompts it dropped.
