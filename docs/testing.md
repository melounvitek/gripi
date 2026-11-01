# Testing

Run the Go gateway, native Node frontend/demo checks, shell script tests, and native Pi extension tests with:

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
- `pi-extension-check` runs Gripi's tree and worker extensions in the installed Pi; the worker tests use a scripted model and a fake helper. Because `mise run test` includes it, Pi CLI must be on `PATH` for tests and CI.
- `desktop-check` and `e2e` need npm dependencies.

For race detection, static analysis, and vulnerability checks, also run:

```sh
go test -race ./...
go vet ./...
govulncheck ./...
```

## Workers over SSH

The worker tests run the helper under `systemd-run --user` on this machine when a systemd user manager is available, and skip otherwise. Tests of the real `ssh`, with saturated channels, a link that stalls in one direction, a master connection that dies and the forward of main's `gripi` socket over a stale socket file, are opt-in:

```sh
GRIPI_TEST_SSH=localhost go test ./internal/worker/ -run RealSSH -v
```

`GRIPI_TEST_SSH` can name any worker that `ssh` reaches without a prompt and that has a systemd user manager. The tests install the helper in `GRIPI_TEST_SSH_DIR` there (`~/gripi-helper-test` by default) and remove it afterwards. When the worker user's home folder differs from yours, the helper runs in the [private view](configuration.md#setting-up-a-worker) of your home folder, and the tests check that sessions and copies see the worker user's home folder at yours, and that systemd's watchdog stops a helper that hangs there.

The tests of the private view itself, in `internal/worker/view`, skip where unprivileged user namespaces are blocked, such as on Ubuntu. To run them, and the helper's tests under systemd in the view, on a worker without Go, build both with `CGO_ENABLED=0 go test -c` and Gripi with `CGO_ENABLED=0 go build`, copy them there, and run the helper's tests with `GRIPI_TEST_GRIPI` naming that Gripi and `GRIPI_TEST_VIEW_HOME` naming a home folder that the view should show, such as `/home/alice`.

The mirror's test with three parallel commit loops checks 8 barriers; for 100, run:

```sh
GRIPI_MIRROR_STRESS=1 go test ./internal/worker/mirror/ -run ParallelCommitLoops -v
```

The Docker proxy, which moves a worker session's paths to the worker user's home, has a test that runs the real `docker` CLI and `docker compose` through it against this machine's Docker: bind mounts, a local volume bound to a folder, piped stdin, a terminal, `exec`, `logs -f`, `build` and `compose up --build`, with main's home folder at a path that holds another user's files, which containers must not see, also through symbolic links, dot segments and lowercase JSON fields. It pulls `alpine` if needed and removes what it created, including `alpine` if it pulled it:

```sh
GRIPI_TEST_DOCKER=1 go test ./internal/worker/dockerproxy/ -run RealDocker -v
```

The helper's test with the same variable runs `docker run` in a session of a helper that serves as it does in the view, with main's home folder at such a path:

```sh
GRIPI_TEST_DOCKER=1 go test ./internal/worker/helper/ -run RealDocker -v
```

An end-to-end test moves a repository with a worktree to a real worker and back, with a scripted model, so no real model is called. The gateway's home folder is `/tmp/gripi-e2e/home/vitek`, which the worker lacks, and its Pi folder, state and projects are in that home folder, so the worker's sessions see them in its user's own home folder through the private view. On the way, it checks that main's copies of the sessions follow the worker's and outlive a gateway restart, that the worker's `auth.json` follows changes to main's without its refresh tokens, that a file edited on main is saved aside, and that a worktree made on the worker runs its sessions there. It then freezes the ssh master connection: a few seconds cost nothing, while half a lease ends the running step with an unknown outcome, and the repository continues on main once the worker is gone. The next move keeps aside on the worker what main never got, and after the master is killed, the next prompt of a session whose Pi ran there connects again and starts its Pi there. It needs another machine, because the worker must not share main's files:

```sh
GRIPI_TEST_WORKER=gripi@beelink.example.ts.net go test ./cmd/gripi -run RealWorker -v
```

It installs the helper in `~/gripi-e2e` on the worker, which also gets what the worker has of the gateway's home folder, and removes it and `/tmp/gripi-e2e` on both machines afterwards.

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

The runner builds and starts a separate gateway with a temporary home, sessions, state, and port, then removes them. It does not touch `gripi.service` or your own Gripi and Pi data. It then does the same for the `hermes` and `hermes-touch` projects with a second gateway that also runs a fake Hermes Agent (`node e2e/support/managed_server.mjs --hermes`), so the other projects keep testing a gateway without Hermes.

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
