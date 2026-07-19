# Native Pi bash mode

## Goal

Support Pi's native `!` and `!!` composer behavior through Pi RPC:

- `!command` executes directly and includes its result in later model context.
- `!!command` executes directly but excludes its result from model context.
- Commands remain available while the agent is running, can be cancelled, and persist in Pi's native session format.

## Constraints

- Execute only through Pi RPC; never spawn a gateway-owned shell.
- Preserve Pi-owned `bashExecution` entries and keep gateway-only runtime state outside session files.
- Existing authorized workspace users may use bash mode.
- Pi RPC does not stream bash output, so show running state followed by the completed result.
- Keep server-rendered history and live rendering aligned.
- Touch controls must activate on first tap.

## TDD rounds

1. Add RPC bash execution, cancellation, and independent active-bash state.
2. Route composer bash commands with native syntax and cancellation semantics.
3. Render native `bashExecution` entries in full and indexed history.
4. Add live browser execution, restored state, and touch-safe cancellation.
5. Add E2E coverage and security/configuration documentation, run the full suite, and complete independent reviews.
