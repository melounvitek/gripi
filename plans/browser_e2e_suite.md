# Browser E2E contract suite

Status: completed

## Goal

Add an implementation-agnostic browser happy-path suite which can exercise the current Ruby gateway or a future replacement. The deterministic suite uses a fake Pi RPC subprocess; a separate opt-in smoke test uses the real installed Pi CLI.

## Constraints

- Browser specs interact only through the UI and HTTP application boundary.
- Pi fixtures retain native Pi JSONL shapes and the fake speaks Pi RPC over stdin/stdout.
- Managed startup is isolated behind one current-implementation adapter.
- Never touch or restart `gripi.service` or use the user's real Gripi/Pi session state.
- Support a full desktop Chromium suite and focused mobile Chromium coverage.
- Support both managed local execution and an externally started disposable target.
- Prefer accessible locators and minimal production changes.

## TDD rounds

1. Harness, isolated fixtures, managed/external modes, fake RPC process contract, and browser approval.
2. Persisted session listing, search/selection/pinning, and mobile drawer coverage.
3. Prompt lifecycle: optimistic user message, live tool/assistant rendering, idle state, persistence, and reload.
4. New-session pending-path lifecycle and first prompt.
5. Active-run steer, follow-up, and abort controls.
6. Model/thinking settings and extension UI.
7. External-target safety, opt-in real-Pi smoke, CI, and documentation.

## Validation

- Managed Chromium suite: 3 fake-Pi support tests and 10 browser tests
- Ruby suite: 915 tests and 6,199 assertions
- Electron suite: 37 tests
- Optional real-Pi request intentionally not executed

## Deferred

- Image uploads and path completion
- Slash commands
- Session tree navigation, fork, and clone
- Older-history loading and conversation find
- Multi-user token isolation
- Electron, Firefox/WebKit, and visual regression tests
