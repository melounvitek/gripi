# PC access: let Gripi's agents use the desktop app's computer

Agents in Gripi sessions on a gateway, which may run on another machine, can use the computer where the desktop app runs (the PC), only with the user's approval on that PC:

1. `gripi pc ssh -- <command>`: `<command>`, such as `git push`, runs on the gateway with the PC's SSH keys. The private keys never leave the PC.
2. `gripi pc run [--cwd DIR] -- <command>` runs a command on the PC as the PC's user and returns its output and exit code.
3. `gripi pc run --root …` runs it as root on the PC.

## Decisions

- **Outbound connection.** The desktop app's main process long-polls the gateway, so the gateway never connects to the PC (NAT is fine). The gateway has no WebSocket support and doesn't need any.
- **Opt-in.** Off by default. **File → Allow PC Access for This Server** turns it on per server, after a warning.
  - The warning also says that the server sees every public key in the PC's SSH agent, with its comment.
  - The app offers it only for `https://` URLs and loopback `http://` ones.
  - It stays on only for that server's origin, so a changed URL, including `applyLaunchUrlOverride`, turns it off.
  - It works only while the app is open.
- **Where the gateway refuses it.** The gateway refuses PC access:
  - in `GRIPI_MULTI_USER_MODE`, because agents there share one OS user, so owner checks can't stop one user's agent from acting as another's;
  - with access approval disabled, because `enforceBrowserAccess` then lets any client through;
  - over plain HTTP from anywhere but loopback, even with `GRIPI_ALLOW_INSECURE_REMOTE_HTTP`, because an on-path attacker could slip a signing request into a poll during a grant. The user's gateway is behind Tailscale Serve's HTTPS, which passes.
- **The app owns approvals.** Approvals are native dialogs (`dialog.showMessageBox`) of the main process. The gateway's page takes no part, and nothing is added to `gateway_preload.js`.
- **The OS asks for the password.** After the user approves a dialog, polkit's dialog confirms it is them, through `pkexec --disable-internal-agent /usr/bin/true`. Gripi never asks for the PC's password itself, so a fake prompt drawn by a compromised gateway page has nothing to imitate. The user agreed to this instead of sudo's own password prompt.
  - It follows polkit's policy for running programs as root. By default that asks for an admin password every time, which on the user's PC is their own.
  - A local polkit rule that skips the password skips it here too. The docs say so.
  - A dedicated polkit action would show a clearer message than "Authorize running '/usr/bin/true'", but needs root to install. → `TODO.md`.
- **The root runner is polkit, not sudo.** `--root` runs through `pkexec`, so it follows polkit's rules rather than sudoers. This follows from the OS-owned prompt, because Gripi never has the password to give sudo.
- **SSH runs on the gateway.** ssh and Git run on the gateway, and the PC only signs SSH login requests that it has checked. There are no per-host profiles on the PC.
- **Linux only for now.** On the user's Omarchy (Arch, Hyprland), `pkexec true` shows a working polkit dialog. That machine has no SSH agent yet, so the docs cover setting one up. The menu item is hidden on macOS, and macOS support goes to `TODO.md`.
- **Like `gripi sudo`, Gripi only.** Pi CLI in a terminal has no PC to ask. This diverges from native Pi in the same way `gripi sudo` does, so the docs say so.

## How it works for the user

Setup, once:

1. Turn on **File → Allow PC Access for This Server** in the desktop app.
2. Have an SSH agent with the keys loaded. On Arch:
   ```
   systemctl --user enable --now ssh-agent
   export SSH_AUTH_SOCK=$XDG_RUNTIME_DIR/ssh-agent.socket
   ssh-add
   ```
   Put the export in the shell's startup file too. The app uses `SSH_AUTH_SOCK`, falling back to `$XDG_RUNTIME_DIR/ssh-agent.socket`.
3. The hosts the agent connects to must be in the PC's `~/.ssh/known_hosts` (or `/etc/ssh/ssh_known_hosts`).

Prompts, shown one at a time with a notification. Deny is the default and the cancel button, and a request nobody answers in 2 minutes is denied:

```
home-server wants to use your SSH key
Session:  Fix login redirect
Key:      ED25519 SHA256:3f9c… (vitek@laptop)
Server:   github.com (in your known_hosts)
Login:    git
                 [Deny]  [Allow once]  [Allow for 5 minutes]

home-server wants to run a command on this computer
Session:  Fix login redirect
Command:  /usr/bin/systemctl --user restart pipewire
Folder:   /home/vitek
As:       you            (as root: "As: root")
                 [Deny]  [Run]
```

- **After approval:** each Allow or Run is followed by the polkit password dialog. For root, that dialog is the one that runs the command.
- **The 5-minute grant** covers this link, session, key, host key and login, for 5 minutes from approval. It is kept only in the app's memory.
  - The docs say plainly, next to the option, that during those 5 minutes anything on the gateway that claims this session can use the key on that host without asking. Session names can be faked, as with `gripi sudo`.
- **Commands** are approved every time.

## Gateway (Go)

`internal/server/pc_routes.go`, modelled on `sudo_routes.go`.

**In-memory link state:**
- the current link ID and its last poll time;
- queued messages;
- requests waiting for replies.

Each app process makes a fresh link ID per server, kept only in memory, so no work carries over a restart.
- Another link ID is refused while the current one has polled within 35 s, so two apps don't push each other out.
- On quit, the app releases its links, so a normal restart doesn't wait. After a crash, the new process waits at most 35 s.
- Requests and replies of an old link never reach a new one.

**Browser routes, behind `enforceBrowserAccess`:**
- `POST /pc/link` holds for up to 25 s and returns the queued messages (requests and cancels).
- `POST /pc/link/reply` answers one request by link ID and request ID.
- `POST /pc/link/close` releases the link on quit.
- They require a JSON body and a custom header (`X-Gripi-PC-Link`), and refuse any request with an `Origin` or `Sec-Fetch-*` header. There's no CORS, so a page can't play the link, whether it's the gateway's own page hijacked or another browser.
- They need HTTPS, with the same condition as `enforceSecureRemoteTransport`: `secureTransport`, or a loopback client with no trusted forwarded scheme. So a proxy on the gateway can't forward remote HTTP as loopback.

**Socket routes for the CLI (`registerLocalRoutes`):** `POST /pc/run` and `POST /pc/ssh-agent`.
- They use the session checks of `app.sudo`: `validOwnedSessionPath`, `knownOrPendingSession`, `canonicalRPCSessionPath`.
- They refuse at once, with a clear message, when PC access is refused (multi-user mode, access approval disabled) or no link is live.
- They wait for the reply, and fail if the link stops polling.
- When the CLI's request ends (`request.Context()`), they queue a cancel.

**Reuse:** `shellWords`, `cappedBuffer` and `SudoResult` for output (16 MiB per stream), `SudoTimeout` for the approval wait, `randomBrowserToken` for IDs, and the session name as `notifySudo` gets it.

`cmd/gripi/pc.go`, plus an entry in `cli.go` and help text in the style of `sudoHelp`:

- **`gripi pc run [--root] [--cwd DIR] -- <command>`** follows `runAsRoot`: `runningSession`, `newGatewayClient`, `client.do`. `--cwd` is a PC path and defaults to the PC user's home folder. It reads no input.
- **`gripi pc ssh -- <command>`** makes a temporary 0700 folder with a socket and runs `<command>` with `SSH_AUTH_SOCK` set to it, keeping its exit code.
  - Each message of each agent connection goes to `POST /pc/ssh-agent` with the session, a connection ID and the message (at most 256 KiB).
  - Reads are bounded, and the request's context ends with its agent connection. It doesn't go through `gatewayClient.do`, which replaces the caller's context.
  - A closed connection is reported too.
  - It doesn't change Pi's environment or the adapters.
- **Exit codes:** the command's own once it ran, otherwise 1 with a `gripi pc:` message (not connected, denied, timed out, refused), and 2 for usage errors.

## Protocol (phase 1)

**CLI → gateway**, on the Unix socket (Local routes): `POST /pc/ssh-agent`.
- Form fields:
  - `session`: `PI_SESSION_FILE`, or `hermes:<id>` as `runningSession` gives;
  - `connection`: a random hex ID per agent connection, made by the CLI;
  - either `message`, one agent message body without its 4-byte length, in standard base64 and at most 256 KiB decoded, or `close=1`.
- `200 {"message": "<base64 reply body>"}`, or `200 {}` for a close.
- Errors are `{"error": "…"}`:
  - 400 bad input;
  - 403 PC access refused (multi-user mode, access approval disabled);
  - 404 no session of yours;
  - 503 no PC connected, or the link was lost or replaced;
  - 504 no reply in time (`PCReplyTimeout`, 5 min, which tests shorten).
- The gateway also passes on the desktop's own `error` replies, as 502.

**Desktop → gateway**, on the browser routes.
- Every request needs:
  - `Content-Type: application/json`;
  - `X-Gripi-PC-Link: 1`;
  - no `Origin` and no `Sec-Fetch-*` header;
  - HTTPS, or loopback without a trusted forwarded scheme.

  Without them, or in multi-user mode, or with access approval disabled, the answer is 403 `{"error"}`.
- `POST /pc/link`, body `{"link": "<id>"}`, answers `200 {"messages": [...]}` as soon as messages are queued, or empty after 25 s (`PCLinkPoll`). Another link polling within 35 s (`PCLinkStale`) gets 409.
- `POST /pc/link/reply`, body `{"link", "id", "message": "<base64>"}` or `{"link", "id", "error": "<text>"}`, answers `200 {}`, 409 when the link isn't the current one, or 404 for an unknown request (already cancelled or timed out).
- `POST /pc/link/close`, body `{"link"}`, releases the link if it is the current one, and fails its waiting requests with 503.

**Messages**, gateway → desktop:
- `{"type": "ssh", "id", "session", "session_name", "connection", "message"}` needs a reply with that `id`. `session` is the canonical session path.
- `{"type": "ssh_close", "session", "connection"}` needs no reply.
- `{"type": "cancel", "id"}` is sent when the CLI's request ended before the reply came. It needs no reply.
- The desktop keys agent connections by `session` and `connection` together.

**CLI agent replies:** on any gateway error, the CLI answers ssh with `SSH_AGENT_FAILURE` (one byte, 5). It prints each distinct error once to stderr as `gripi pc: <error>`.

## Desktop (Electron)

**`electron/pc_access.js`** has no Electron imports, so `node --test` can test it. `main.js` injects the request function, dialogs, auth, `spawn` and the clock.
- **Link loop**, for each server with access on: another poll as soon as one returns, and backoff on errors.
- **Requests** are handled concurrently while dialogs stay one at a time. More than 5 waiting are denied.
- **Before signing or spawning**, it checks again after each dialog that access is still on, the link is the same and the request isn't cancelled.
- **Clearing:** pending approvals and grants are cleared when access is turned off, the URL changes, the link is lost, or the app closes.
- **Runners:** `spawn` with no shell, `stdio: ['ignore', 'pipe', 'pipe']`, `detached: true`, and the app's own environment, not the gateway's. With no terminal, `pkexec` fails closed without a polkit agent. A cancel kills the process group.
- **Display:** control and bidirectional characters are escaped, as `shellWords` does.

**`electron/ssh_agent_guard.js`** checks each relayed agent connection on the PC.
- Each relayed connection has its own upstream connection to the local agent, so the bind and the signature go over the same one.
- A connection may close without sending anything, as `ssh -A` does to check the agent.
- Session IDs are 32, 48 or 64 bytes, depending on the key exchange.
- **Message types.**
  - Request identities (11) is passed on. Every key in the agent is listed.
  - Sign (13) is checked.
  - The extension message (27) is allowed only as `session-bind@openssh.com`.
  - Anything else (add, remove, lock and so on) gets `SSH_AGENT_FAILURE`.
- **session-bind.**
  - Parse the host key, session ID, signature and `is_forwarding`.
  - Verify the host key's signature over the session ID.
  - Only the connection's first bind counts.
  - When that first bind has `is_forwarding` set, refuse it and everything after it on that connection. A forwarded connection starts with that bind, followed by the inner session's own bind, which would otherwise pass (phase 0). As `is_forwarding` isn't signed, a missing flag doesn't prove there was no forwarding.
  - Refuse any bind after the first.
  - Pass the bind on to the local agent, and sign even when that agent refuses it.
    - OpenSSH's agent refuses to sign with keys added with `ssh-add -h` on a connection it didn't accept a bind for, so those keys keep their limits.
    - Agents without session-bind have no destination limits to keep.
    - Requiring the agent's success would refuse those agents for no gain.
- **Sign request.** Accept only an SSH user-auth request on a bound connection:
  - the same session ID;
  - message 50;
  - service `ssh-connection`;
  - method `publickey` or `publickey-hostbound-v00@openssh.com`, the latter with the bound host key;
  - the has-signature boolean set;
  - the same key as the request, with an algorithm that matches the key and the request's flags (the RSA SHA-2 flags only for RSA keys);
  - no trailing data.

  Anything else, `ssh-keygen -Y sign` too, is refused.
- **Host.** Look the bound host key up, as a raw key, in `~/.ssh/known_hosts` and `/etc/ssh/ssh_known_hosts`.
  - `@revoked` wins over everything, and `@cert-authority` lines are no host pins.
  - Plain entries give the host name as a label, and hashed ones show as "known host".
  - Refuse an unknown key, and say how to add it, naming the key's type and fingerprint.
  - The host key is the type that the gateway's ssh negotiates, which follows the gateway's own `known_hosts`. When the PC knows that host by another key type, it is refused as unknown. The docs say so.
- **Host key algorithms:** `ssh-ed25519`, `ecdsa-sha2-nistp256/384/521`, `rsa-sha2-256/512`, using Node's `crypto.verify` with keys built as JWK from the SSH wire format. Each one has a test with a tampered signature.

**`electron/main.js`:**
- the menu checkbox with the warning, and a menu rebuild on `gateway-config:activate`;
- starting and stopping links;
- gateway requests from the main process with Node's `http`/`https` and a `Cookie` header built from the partition's cookies (`session.cookies.get`, which returns HttpOnly ones too):
  - Node's client never follows redirects.
  - Electron's `net.request` and `ses.fetch` are out, because they send `Sec-Fetch-Site: none` (phase 0).
  - `Set-Cookie` replies aren't saved back, which the link doesn't need.
  - Chromium's proxy and certificate settings don't apply. Tailscale Serve's certificates are public ones.
- the dialogs, with the main window as parent, plus a `Notification` and focusing the window;
- auth: `pkexec --disable-internal-agent /usr/bin/true`. Exit 0 means polkit authorized it under local policy, and anything else is a denial with its reason.

**`electron/gateway_config.js`:** keep `pcAccessOrigin` in `normalizeGatewayOrNull`. Access is on only while `pcAccessOrigin` equals the URL's origin.

**`package.json` `desktop:check`:** `node --check` for the new files.

## Phases

Each phase works on its own and is documented. TDD rounds are committed one by one.

**Phase 0: spikes. Done, nothing committed.** Scripts are in `tmp/pc_spike/` (Git-ignored); `tmp/pc_spike/ssh/run/relay.mjs` is a reference for the parsers and verification.
1. **Electron 43 requests.**
   - Every page POST, including form posts, `sendBeacon`, `<a ping>` and service worker fetches, sends `Origin`. Page GETs don't, so the link needs a JSON body.
   - `net.request` and `ses.fetch` from the main process send `Sec-Fetch-Site: none`. Node's `http.request` sends neither header and passes the strict rule.
   - `ses.cookies.get` returns HttpOnly cookies.
   - A followed redirect re-sends the cookies and the custom header, so the app never follows one.
2. **OpenSSH 10.2 through a relay.**
   - Each connection sends a bind first, then the identities request, then a sign. A bind always comes before the sign. Each git operation is its own connection.
   - The method is `publickey-hostbound-v00@openssh.com` when the server offers it, with the bound host key.
   - RSA keys sign with flags 4 (`rsa-sha2-512`) or 2 (`rsa-sha2-256`).
   - Verification passes for ed25519, ECDSA P-256/384/521 and RSA SHA-2, and fails for each tampered signature.
   - `ssh-keygen -Y sign` sends a sign request without a bind (SSHSIG data), which is refused.
   - Agent forwarding and session ID lengths changed the guard's rules above.
3. **`pkexec` 127 / polkit 128 on Omarchy.**
   - Confirmed: exit 0, and `--keep-cwd` keeps the folder. Cancelled, or a wrong password then cancelled: exit 126 "Request dismissed".
   - Two approvals in a row both ask.
   - Killing pkexec while its dialog is open closes the dialog, and the command doesn't run.
   - The watchdog wrapper closes the dialog when the app's pipe closes, and the command doesn't run.
4. **Hyprland 0.56.**
   - The app's window and its dialog come to the front by themselves for a request that arrives while the user is on another workspace.
   - Enter and Escape both answer Deny.

**Phase 1: SSH keys.**
1. Gateway link, queue and socket routes, and the `gripi pc ssh` relay.
2. Desktop opt-in, config and link loop.
3. SSH guard and known hosts.
4. Dialogs, pkexec auth and grants.
5. Docs and skill.

**Phase 2: `gripi pc run`** as the PC user.
1. The run dialog, the runner with output caps and cancel, and a cwd that must be absolute and exist (home by default). `argv[0]` is resolved on the PC's PATH, and the dialog shows the resolved path.
2. Workers: both socket routes go into `workerRoutes`, so sessions on workers can use the PC too.

**Phase 3: `--root`.**
- It runs `pkexec --disable-internal-agent --keep-cwd <resolved executable> <args>` with the spawn's cwd. polkit's dialog, which names the executable, is the auth step.
- **Watchdog.** pkexec runs under a small `sh` wrapper that runs without root. The wrapper keeps a pipe from the app open, and kills pkexec when the pipe closes. So an app that crashes while polkit's dialog is open cancels it.
  - This works because pkexec keeps the user's real UID until it execs the command.
  - pkexec, being setuid, drops a parent-death signal, which is why a wrapper is needed.
- pkexec's own refusals (126 and 127) come back with pkexec's stderr as they are.
- A cancel kills `pkexec` while it still waits for the password, but a root process that has started can't be killed from the user's app. This is documented like the `gripi sudo` TODO.
- **Gate:** the spike showed that a crash while polkit's dialog is open doesn't start the command. The watchdog is still best effort: a crash at the very moment polkit authorizes can still let the approved command start. The user decides whether that window is acceptable before phase 3, or root goes to `TODO.md`.

## Trust model

- **Who can ask:** anything running as the gateway's OS user that reaches the gripi socket, as with `gripi sudo`. The session is best effort, because `PI_SESSION_FILE` can be faked.
- **What a compromised gateway or page can't do through the link:**
  - create an approval or a grant;
  - see the PC password or private keys;
  - get signatures other than SSH logins to a host key the PC knows;
  - run a command other than the one shown;
  - use the PC while the app is closed or access is off.
- **What it can do:**
  - lie about the session name;
  - spam prompts, which the cap limits;
  - use a granted key on that host until the grant ends;
  - see the output of approved commands.
- **Beyond the link:** once a command is approved, it can do anything the PC's user, or root, can, which no longer depends on the link. An SSH connection opened during a grant, or a `ControlMaster`, can outlive the grant. Expiry only stops new signatures.

## Tests

- **Go, `cmd/gripi/pc_test.go`**, following `sudo_test.go` and `fakePiGateway` (`cmd/gripi/gateway_test.go`) with access approval on. A test client plays the desktop by polling `/pc/link` as an approved browser. It covers:
  - request contents;
  - reply → output and exit code;
  - deny;
  - no link → immediate failure, and a link that dies mid-request;
  - a second link refused while the first is live, and accepted after it is released;
  - an old link's requests and replies never reaching a new one;
  - plain remote HTTP refused, even when insecure remote HTTP is allowed, including a loopback proxy peer with `X-Forwarded-Proto: http`;
  - cancel when the CLI exits;
  - `Origin` and `Sec-Fetch-Site` refused;
  - multi-user and access approval disabled refused;
  - the worker route (phase 2);
  - `gripi pc ssh -- ssh-add -l` listing a real `ssh-agent`'s keys through the relay.
- **Node, `electron/ssh_agent_guard.test.js`:**
  - Malformed and truncated frames.
  - Tampered signatures for each algorithm.
  - With real OpenSSH: a throwaway `sshd` (own config, high port, current user) with host keys of each algorithm, an `ssh-agent`, and `ssh` through a test relay that wraps the guard.
    - A known host succeeds.
    - A key added with `ssh-add -h` for another host is still refused by the agent, though the guard accepted the host.
    - `ssh -A` through the guard: the empty check connection passes, and a forwarded connection is refused after its first bind.
    - A key exchange with a 48- or 64-byte session ID passes.
    - An unknown, revoked or cert-authority-only host, the wrong key or login, an unbound sign, a forwarding bind, `ssh-keygen -Y sign`, and add, remove or lock are all refused.

  The OpenSSH tests are skipped when `sshd` is missing.
- **Node, `electron/pc_access.test.js`**, with an injected request function, dialogs and clock:
  - the link loop (requests, cancels, reconnect);
  - Deny by default;
  - grant scope and expiry;
  - clearing on disable and link loss;
  - the check again after a dialog;
  - the cap;
  - a failed auth denying;
  - runner output caps and cancel.
- **Full chain, `e2e/specs/pc_access.spec.js`**, with the managed gateway (`e2e/support/managed_server.mjs`) and the desktop's link core running in Node with approvals injected. It runs `gripi pc ssh -- git fetch` and `git push` against a throwaway `sshd` and bare repository. Skipped without `sshd`.
- **`electron/gateway_config.test.js`:** the opt-in persists, and resets when the origin changes.
- **Manual checklist on Omarchy**, because native dialogs and polkit can't run in CI.

## Docs

- **`docs/configuration.md`:** a new section "Using your PC" after "Running commands as root". It covers:
  - setup, including the SSH agent on Arch and known hosts;
  - the prompts and what a 5-minute grant allows;
  - polkit rather than sudo;
  - the trust model and the limits.
- **`README.md`:** the command line list and a bullet in the Security section.
- **`pi_skills/gripi/SKILL.md`:**
  - use `gripi pc ssh --` when the gateway lacks a key (`Permission denied (publickey)`) or the user asks for it;
  - use `gripi pc run` for work on the user's computer;
  - never retry after a deny.
- **`TODO.md` follow-ups:**
  - macOS;
  - multi-user once agents are isolated;
  - commit signing with `ssh-keygen -Y sign`;
  - choosing which keys to expose;
  - trusting unknown host keys from the dialog;
  - host certificates;
  - a dedicated polkit action;
  - killing root processes on cancel;
  - showing a waiting PC request in the sidebar.
