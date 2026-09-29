# Testing

Run the Go gateway, native Node frontend/demo checks, shell script tests, and native Pi extension bridge test with:

```sh
mise run test
```

The individual checks are available as:

```sh
mise run test-go
mise run frontend-check
mise run scripts-check
mise run pi-extension-check
mise run desktop-check
mise run fake-pi-check
```

- `frontend-check` runs native Node tests for the browser modules, controller races, and the static demo, then syntax-checks the JavaScript. It needs no `node_modules`.
- `scripts-check` tests the setup, launcher, restart, password, and desktop installer scripts.
- `pi-extension-check` loads the tree extension through the installed Pi package. Because `mise run test` includes it, Pi CLI must be on `PATH` for tests, CI, and self-update validation.
- `desktop-check` and `e2e` need npm dependencies.

For race detection, static analysis, and vulnerability checks, also run:

```sh
go test -race ./...
go vet ./...
govulncheck ./...
```

## Browser contract suite

The Playwright suite exercises Gripi only through a browser. Its fixtures use Pi's native JSONL session format, and its deterministic Pi replacement is an independent RPC subprocess. Browser specifications do not import gateway packages or call internal handlers directly.

Install Chromium once after installing npm dependencies:

```sh
npx playwright install chromium
```

Run the managed suite:

```sh
mise run e2e
```

The runner builds and starts a separate gateway with a temporary home, sessions, state, and port, then removes them. It does not touch `gripi.service` or your own Gripi and Pi data.

A desktop Chromium project covers the main flows; a mobile Chromium project covers the session drawer and a complete prompt. Traces and screenshots are kept only for failed tests.

## External implementation

The same specifications can target another implementation:

```sh
GRIPI_E2E_BASE_URL=http://127.0.0.1:4567 \
GRIPI_E2E_ADMIN_PASSWORD=... \
npm run test:e2e:external
```

The target must be disposable and seeded with the contract fixtures. Generate them with:

```sh
node e2e/fixtures/seed.mjs /tmp/gripi-e2e-target
```

The command prints the fixture paths. Configure the target to use the printed session root and configured-directory file plus isolated gateway state paths. The fake must inherit `GRIPI_E2E_SESSIONS_ROOT` with the printed session root so it can persist newly created sessions.

Configure the Pi command as a Node/script pair: `GRIPI_NODE=$(command -v node)` and `GRIPI_PI=$(pwd)/e2e/support/fake_pi.mjs`. The target should invoke the same script with Node and pass Pi RPC arguments through unchanged. Browser approval may be enabled with the supplied admin password or disabled by the target.

Before any mutating specification runs, the setup project requires the visible `E2E Contract Ready` session and confirms that live status reports the `e2e/fixture-model` fake. This prevents accidentally running the suite against a personal gateway or a seeded target still connected to real Pi.

Use one worker and no retries while the suite shares a target. Each scenario has its own seeded session, but retries could still encounter state left by the failed attempt.

## Optional real-Pi smoke

To make one real model request through the installed and authenticated Pi CLI:

```sh
npm run test:e2e:real
```

This is opt-in because it needs network access, credentials, and an available model, and it may cost money. It uses the user's configured Pi agent directory for authentication and model settings, but stores the smoke-test session and all Gripi state under a temporary home. It is not run in CI.

## Memory benchmark

On Linux, measure the gateway’s memory and request times with:

```sh
mise run benchmark-go
```

It builds the gateway, starts it on a free loopback port with a temporary session fixture, sends 100 requests, and samples the memory of the whole gateway process tree. It does not use `gripi.service` or start real Pi.
