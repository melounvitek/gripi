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
- **The computer you use (phase 2).** Several computers can have PC access on at once.
  - A request goes to the computer where the user last used that session: sent a message there, or clicked or typed in it in a desktop app with PC access on.
  - Reading or scrolling on a phone doesn't move it. Sending a message from a client without PC access, such as a phone, does, and then requests are refused.
  - It never falls back to another computer.
- **Visible on every device (phase 3).** While a computer asks for approval, every Gripi client showing that session says so, with a Cancel, and a Web Push notification goes out. Approval itself stays on the computer.

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
- `POST /pc/link`, body `{"link": "<id>", "open": true}` for a link's first poll and `{"link": "<id>"}` after, answers `200 {"messages": [...]}` as soon as messages are queued, or empty after 25 s (`PCLinkPoll`). A poll carries at most 64 messages and 2 MiB of agent messages, and the rest wait for the next one. (Phase 4: at most 4 MiB of the messages' JSON, but a single message may have more.) Another link polling within 35 s (`PCLinkStale`) gets 409, and so does a poll without `open` of a link that the gateway never had or already ended (a restart, going stale, a close or `EndPCLink`). When the link ends (closed, or `EndPCLink` before the gateway drains on shutdown), an open poll gets 503, and so do new polls after shutdown began.
- `POST /pc/link/reply`, body `{"link", "id", "message": "<base64>"}` or `{"link", "id", "error": "<text>"}`, answers `200 {}`, 409 when the link isn't the current one, or 404 for an unknown request (already cancelled or timed out).
- `POST /pc/link/close`, body `{"link"}`, releases the link if it is the current one, and fails its waiting requests with 503.

**Messages**, gateway → desktop:
- `{"type": "ssh", "id", "session", "session_name", "connection", "message"}` needs a reply with that `id`. `session` is the canonical session path.
- `{"type": "ssh_close", "session", "connection"}` needs no reply.
- `{"type": "cancel", "id"}` is sent when the CLI's request ended before the reply came. It needs no reply.
- The desktop keys agent connections by `session` and `connection` together.

**CLI agent replies:** on any gateway error, the CLI answers ssh with `SSH_AGENT_FAILURE` (one byte, 5). It prints each distinct error once to stderr as `gripi pc: <error>`.

## Desktop (Electron)

**`electron/pc_access.js`** has no Electron imports, so `node --test` can test it. `main.js` injects the request function and dialogs, and tests also auth, `spawn` and the clock.
- **Link loop**, for each server with access on: another poll as soon as one returns, and backoff on errors. A link's first poll opens it.
- **Requests** are handled concurrently while dialogs stay one at a time. More than 5 waiting are denied.
- **Before signing or spawning**, it checks again after each dialog that access is still on, the link is the same and the request isn't cancelled.
- **Clearing:** pending approvals and grants are cleared when access is turned off, the URL changes, the link is lost, or the app closes.
- **A lost link** is any failed poll: a network error, any status but 200, or a malformed body. The app then aborts that link's requests, dialogs and polkit check, closes its agent connections, clears its grants, sends a best-effort close, and polls again with a new link ID after backing off.
- **Limits** against a compromised gateway:
  - an 8 MiB response cap and a 40 s deadline per request;
  - messages checked before use;
  - duplicate in-flight request IDs refused;
  - 32 agent connections per link, and 8 waiting messages per connection;
  - 64 messages per poll, and 256 requests in progress per link, counting refusals still being sent and the requests of closed connections until they end. A poll over either loses the link, checked for the whole batch before any of it starts, without replies;
  - session names cut to 200 characters for display, and logins over 256 bytes refused.
- **The approval turn** lasts through polkit, until `pkexec` has exited, under one 2-minute deadline from when the dialog appears.
- **Auth:** `pkexec --disable-internal-agent /usr/bin/true`. Exit 0 means polkit authorized it under local policy, and anything else, a cancel too, is a denial with its reason.
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
- gateway requests from the main process with `linkRequest` from `pc_access.js` (Node's `http`/`https`, which the E2E spec uses too) and a `Cookie` header built from the partition's cookies (`session.cookies.get`, which returns HttpOnly ones too):
  - Node's client never follows redirects.
  - Electron's `net.request` and `ses.fetch` are out, because they send `Sec-Fetch-Site: none` (phase 0).
  - `Set-Cookie` replies aren't saved back, which the link doesn't need.
  - Chromium's proxy and certificate settings don't apply. Tailscale Serve's certificates are public ones.
- the dialogs, with the main window as parent, plus a `Notification` and focusing the window;

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

**Phase 1: SSH keys. Done.**
1. Gateway link, queue and socket routes, and the `gripi pc ssh` relay.
2. Desktop opt-in, config and link loop.
3. SSH guard and known hosts.
4. Dialogs, pkexec auth and grants.
5. Docs and skill.

**Phase 2: route to the computer you use. Done.** Details under "Routing (phase 2)".
1. Gateway: one link per approved browser, the use record, routing and refusals, pinned agent connections.
2. Page and desktop: the `used` report on real input and on the user's switches, prompt retries marked, the computer's name on the open poll.
3. Docs and skill.

**Phase 3: show a waiting approval on every device. Done.** Details under "Waiting approvals (phase 3)".
1. Gateway: the status on the waiter, the approvals in the page and live events, Cancel from Gripi, Web Push.
2. Desktop: the status posts from the approval turn.
3. Page: the line above the editor (direction B), and the service worker's focus check.
4. Docs and skill.

**Phase 4: `gripi pc run`** as the PC user. Details under "Running commands (phase 4)".
- Before it: Cancel from Gripi wins until the gateway hears the desktop's `asking: false` or reply, so it can refuse a login that the user just allowed on the PC (phase 3 accepted that for SSH, which only refuses). A run has side effects on the PC, so decide whether the desktop confirms with the gateway before it spawns.
1. The run dialog, the runner with output caps and cancel, and a cwd that must be absolute and exist (home by default). `argv[0]` is resolved on the PC's PATH, and the dialog shows the resolved path.
2. Workers: both socket routes go into `workerRoutes`, so sessions on workers can use the PC too. Routing uses main's session key, and the routes check the session's placement with `reachesSession`.
3. Docs: when the gateway's `~/.ssh/config` names a key for a host with `IdentitiesOnly yes`, `gripi pc ssh` uses only that key there.

**Phase 5: `--root`.**
- It runs `pkexec --disable-internal-agent --keep-cwd <resolved executable> <args>` with the spawn's cwd. polkit's dialog, which names the executable, is the auth step.
- **Watchdog.** pkexec runs under a small `sh` wrapper that runs without root. The wrapper keeps a pipe from the app open, and kills pkexec when the pipe closes. So an app that crashes while polkit's dialog is open cancels it.
  - This works because pkexec keeps the user's real UID until it execs the command.
  - pkexec, being setuid, drops a parent-death signal, which is why a wrapper is needed.
- pkexec's own refusals (126 and 127) come back with pkexec's stderr as they are.
- A cancel kills `pkexec` while it still waits for the password, but a root process that has started can't be killed from the user's app. This is documented like the `gripi sudo` TODO.
- **Gate:** the spike showed that a crash while polkit's dialog is open doesn't start the command. The watchdog is still best effort: a crash at the very moment polkit authorizes can still let the approved command start. The user decides whether that window is acceptable before phase 5, or root goes to `TODO.md`.

## Routing (phase 2)

**Who is who.**
- Each approved browser is an owner: `pushOwner(request)`, a digest of its `gripi_browser` cookie.
- A desktop app's page and its link send the same partition's cookie, so they share an owner, and each computer's app has its own.
- Windows, popups and the Hermes panel of one app share it.

**Links.**
- `pcLinks.current` becomes a map by link ID. Each link is bound to the owner that opened it. Every poll, reply, close and status must match both, so a rotated `gripi_browser` cookie (`adminLogin`) gets 409 and the app opens a new link, rather than carrying the old link's waiters into the new identity.
- `adminLogin` replaces the browser's token, so it ends the old owner's links at once (`pcLinks.endOwner`), failing their waiting requests, rather than leaving them live for 35 s.
- Opening a link gets 409 only while another live link has the same owner, such as a copied profile or an app that crashed and restarted within 35 s. Other owners open freely.
- The open poll may carry `"name"`, the computer's short hostname (`os.hostname()` up to the first dot, at most 64 characters, cleaned to one line). Refusals name the computer with it now, and phase 3's waiting line and Push will too. A link that sent no name is "the user's computer" in refusals.
- Stale timers, replies, closes and cancels work per link. `EndPCLink` ends all of them.

**Use record.** In memory: session key → (owner, time of receipt). It is recorded only when PC access isn't refused (single-user mode, access approval on), for a session the gateway knows (`knownOrPendingSession`) and the browser owns, and never for scratch sessions. The time is when the gateway began to receive the request, and a use is recorded only if it is newer than the current record, so a click elsewhere during a slow upload of a message wins.
- `app.prompt` records the sender for a browser request (`!commandRequest(request)`), from any client, at its start, before anything can refuse it: also before `actionSessionPath`, whose canonicalization can answer a retryable 409 when it contends with a remap, and before a `!` command runs (`runBash`, which returns early) or Pi gets the prompt (`withSynchronizedClient`). So `gripi send`, sub-session reports and workers never count, and a message from a phone records the phone.
  - Automatic retries of a submission (`sendPromptRequest` in `app.js`) follow only a 409 `session_operation_pending`, which the first attempt gets from `app.prompt` after it recorded. So retries carry `retry=1` and never record, and a late retry can't take routing back from a newer use elsewhere.
- `PUT /web-push/presence` with `"used": true` and `focused` records the session for an owner that has a live link. So reading on a phone or in an app without PC access never moves it.
  - The route checks the session like the PC routes do (`knownOrPendingSession`), and resolves a pending key before recording, since `sessionStore.Canonical` doesn't. Every record resolves its key under the lock that the remap's commit holds, so none stays behind on a key that moved.
- The page (`notification_presence.js`) reports `used` on trusted input while it shows a session and is looked at: non-repeating `keydown`, `pointerdown` and `wheel`. Not `touchstart`: `pointerdown` covers touch, and both would report each tap.
  - It reports at once and never defers a use to a later report, which could land after a newer use elsewhere.
  - Clicks and taps (`pointerdown`) always report. Typing and scrolling (`keydown`, `wheel`) report at most every 2 s per session, dropping the rest.
  - Focus changes, visibility changes, reloads, heartbeats and reports after a gateway restart never carry `used`. Gripi's own `focus()` calls don't make trusted input.
  - Input on the controls that open other sessions (the sidebar, the relation tree, the command palette) and keys with Ctrl or Meta (such as Ctrl+1…9) fires while the page still shows the session being left, so it doesn't count. Instead, a switch that the user starts (a sidebar session, the palette, Ctrl+1…9) passes `{ used: true }` through `switchSession` to `sessionChanged`, which reports the session it opens. Background refreshes (`refreshSessionInBackground`) don't. The relation tree's links load a new page, whose session counts from its first input.
- Session keys are canonical. A new session's record under its pending key moves with it, atomically, in `remapPendingRPCClient`'s commit, next to `pendingSessions.Remap`, keeping the newer record if both exist.

**Routing a request** (`/pc/ssh-agent`, and `/pc/run` later):
1. Each agent connection is pinned, by its connection ID, to the link of its first message, with the session key that message used, because the desktop's guard state is per connection. Its later messages and `ssh_close` carry that key.
   - The CLI marks a connection's first message (`open=1`). A message without a pin and without the mark is refused, so a connection whose pin was dropped can't start again on another link. So is `open=1` for a connection ID that has a pin, live or ended.
   - Each later message is routed again, by the session of the connection's first message, wherever a new session moved since, whichever session the message names. If the route changed, such as after a message from the phone between the bind and the sign, the message is refused. Messages already queued finish.
   - A pin to a link that ended stays, so its messages fail rather than starting again on another link.
   - `ssh_close` goes only to the pinned link, with the pinned session key, and drops the pin. Pins are capped: at the cap, those of ended links go, and without any, a new connection is refused, as open ones may still wait for replies.
2. Otherwise, use the session's own record.
3. Without one, walk up `gatewayState.SessionParents()`, bounded and cycle-safe, to the nearest ancestor with a record. A sub-session's own record wins.
4. An owner with a live link gets the request. Anything else is refused with 503, never sent to another link:
   - no record and no live link at all: phase 1's "no computer is connected; …";
   - no record: "nobody has used this session in a Gripi desktop app with PC access since the gateway started; ask the user to open it in the desktop app on the computer that should do this, and click in it";
   - an owner without a live link: "this session was last used on <name>, where the Gripi desktop app isn't connected for PC access; …", with "the user's computer" when its link sent no name. It says "in a Gripi window without PC access, such as a phone or a browser" when no link ever came from that owner.

**No persistence.** A gateway restart ends every Pi turn, and a self-update waits until none runs, so the next turn usually starts with the user's own message, which records the computer. A turn that `gripi send` or a sub-session report starts is refused until the user uses the session. Persisting would add a state file with its remapping and deletion lifecycle for little gain.

**Protocol changes:**
- `POST /pc/link`: the open poll may carry `name`. 409 now means another live link of the same browser, or an unknown link or one of another browser.
- `PUT /web-push/presence`: may carry `"used": true`.
- `POST /prompt`: the page's automatic retries carry `retry=1`.
- `POST /pc/ssh-agent`: a connection's first message carries `open=1`. The CLI and the gateway ship together.
- The messages to the desktop don't change. Only the 503 errors gain the routing reasons.

**Tests.**
- Go (`cmd/gripi/pc_test.go`, with a browser token per fake desktop):
  - two links, where a laptop prompt routes only to the laptop's link and a `used` report from the desk moves it;
  - focus reports, heartbeats without `used`, and `gripi send` don't move it;
  - a phone prompt (owner without a link) is refused, and the refusal names the last computer once its link is gone;
  - a sub-session follows its parent until its own use, and a phone prompt in the sub-session blocks the parent's computer;
  - a presence `used` report under a pending key routes by the real path, also when it comes after Pi saved the session (an internal test, as the fake Pi names its file at once);
  - a phone prompt between two messages of one agent connection refuses the later one, and `ssh_close` reaches only the first link;
  - a connection pinned to an ended link fails, and doesn't move to a new link, also after its pin was dropped at the cap, `open=1` again on a pinned connection is refused, a later message routes by the connection's session also when it names another one, and a new connection beyond the cap is refused while none ended;
  - a `!` command from a browser records, a retry (`retry=1`) doesn't, a message counts also when the gateway refuses it, even with a retryable 409 from finding the session (an internal test), and a click elsewhere during a message's upload wins;
  - the same owner's second link gets 409, a reconnect with a new ID takes over, and a rotated cookie can't use the old link;
  - `adminLogin` ends the browser's old link at once, failing its waiting requests;
  - `used` from a browser without a link, or for a session the gateway doesn't know, records nothing, and neither does a scratch session (an internal test, as nothing else shows it);
  - after a restart, a live link alone isn't enough, and `gripi send` doesn't change that.
- Frontend (`test/frontend_notification_presence.test.mjs`): trusted clicks report `used` at once every time, typing and scrolling at most every 2 s; untrusted events, input on the controls that open sessions, Ctrl or Meta keys, focus changes and heartbeats don't; a user's switch reports the session it opens, not the one being left, and only once on touch.
- E2E (`e2e/specs/pc_access.spec.js`): a click in the session page, then `gripi pc ssh` works; with two approved contexts, a click in B routes to B, and reloading A doesn't move it back. (`e2e/specs/prompt.spec.js`): prompt retries carry `retry=1`.
- Node (`electron/pc_access.test.js`): only the open poll carries `name`.

**Docs:**
- In `docs/configuration.md`, replace "One desktop app can use a gateway at a time" with how routing works, that it never falls back, and that it starts over after a restart.
- `pcHelp` gets the routing refusals.
- `pi_skills/gripi/SKILL.md`: on a routing refusal, ask the user to open the session on the right computer rather than retrying.

## Waiting approvals (phase 3)

When the user is on the phone and a computer asks for approval, the phone must say so clearly.

- **The desktop says when it asks.** When the approval turn starts for a request (in `approveSign`, when its dialog shows, not on a grant hit or while queued), the app posts `POST /pc/link/status` with `{link, id, asking: true, summary, expires_in}`. It posts `asking: false` when the dialog and polkit end.
  - The summary is short text the app builds, such as "SSH login to github.com as git", or for `run` the command.
  - The gateway caps and escapes it, and the body is checked like the other link routes.
- **The status lives on the request's existing waiter**, not in a second registry. It is accepted only for a waiting request of that owner and link, whose session it takes from the waiter. `asking: false` is final, so a late `asking: true` can't bring the line back or send another notification. The deadline is the desktop's real one.
- **Waiting approvals end** with `asking: false`, the request's reply, its cancel, or the link's end.
- **Every client showing the session shows them**, like `sudo_requests`:
  - in the page's initial data (`views.go`) and in the events payload (`rpc_routes.go`);
  - with a quiet line in the conversation's editor area: "Waiting for you on laptop: SSH login to github.com", a countdown and **Cancel**;
  - **Cancel** posts to a browser route that, at once, ends the request's waiter with a refusal ("cancelled in Gripi"), clears the line and queues the request's `cancel` on its link, which closes the dialog. The desktop sends no reply after a cancel, so without ending the waiter the CLI would wait 5 minutes. When the reply won the race, the route answers that the request no longer waits. Cancelling only refuses, so a hijacked page can do no more than refuse.
  - Server-rendered and live rendering both need it (`AGENTS.md`).
- **Web Push**: "Approve on laptop: SSH login to github.com", sent once on `asking: true` to every subscription of a currently approved browser (`completionNotifications.owners`), so one left by a rotated cookie gets nothing.
  - Unlike `notifySudo`, the gateway doesn't skip it when a client shows the session: in single-user mode `notificationPresenceOwner` makes all clients one owner, and the approving desktop focuses itself, which would silence a phone in a pocket.
  - Each device's service worker already skips a notification for a session that this device shows in front (`web_push_worker.js`).
  - For `run`, the notification says "run a command" rather than the command, which would show on a locked screen.
- **Mockups first.** Two or three directions at desktop and phone widths, from the project's real styles. The user picks one before implementation. The user picked direction B: a slim line just above the editor, which stays usable.
- **Notes from the review of phase 2's plan:**
  - In `approveSign`, bail inside the approval turn when the request was aborted while queued, and check the grants again before posting `asking: true`.
  - The browser's Cancel acts only while the waiter is asking, and answers 409 otherwise, so it never aborts an approved operation. It goes through the normal browser routes' same-origin protection, like the sudo cancel.
  - `expires_in` is in seconds, clamped to 1 s … `PCReplyTimeout`.
  - Web Push skips the approving link's own owner, whose app shows its native notification. `web_push_worker.js`'s focus check must also match the `/hermes` and `/` (default session) pages.
  - The B line stays visible outside `.prompt-form`'s sudo and worker hide scope, never takes the editor's focus or binds Escape, can show more than one approval, and keeps cancelled IDs so that polls in flight don't bring them back. It needs server-rendered initial data and a first-tap touch test on Cancel.
  - `live_output_bytes_test.go` needs updating for the new `/events` field.
- **As built:**
  - The line ends only when the gateway's list drops it, so a device whose clock is off still shows it; its clock only drives the countdown.
  - A new session's waiting request moves with its key when Pi saves it, like the use record.
  - The notification names its session while it stays within 3 KiB encoded, and the service worker asks each focused page which session it shows, as a page's URL may not name it (`/`, `/hermes`) or name an older key.
  - The docs say that Cancel can still refuse a login allowed on the PC a moment before, and doesn't take back a 5-minute grant.
- **Tests:**
  - Go:
    - status route checks, and status refused for another owner, link or unknown request;
    - the line ends on reply, cancel and link end;
    - a late `asking: true` after `asking: false` or a cancel doesn't come back;
    - a cancel from the browser route ends the CLI's request at once;
    - Push goes out while the approving desktop shows the session.
  - Frontend: live rendering.
  - E2E: a phone-sized context sees the line and cancels, which aborts the fake desktop's dialog.
- **Not in scope:** a mark in the sidebar and `gripi list`, which `TODO.md` already has for `gripi sudo`; it gets PC requests added.

## Running commands (phase 4)

**The race with Cancel.** The user decided that the desktop confirms with the gateway right before it spawns.
- After the dialog and polkit, still in the approval turn, the app posts `POST /pc/link/status` with `{link, id, starting: true}`, and spawns only on 200.
- The gateway answers 200 only for a command whose waiter is live: not cancelled, timed out, or ended by gripi or the link's end, also when gripi's request ended or the timeout fired a moment before the gateway removed the waiter. From then on:
  - the waiter no longer asks, as after `asking: false`, so the line and Cancel go;
  - a browser Cancel gets 409 "The command already runs.", never a cancel;
  - the waiter waits for the reply without `PCReplyTimeout`, as a command may run long. gripi's end or the link's still end it, and the app then kills the command.
- Otherwise it answers 404, or 409 for a link that isn't open, and the app doesn't run the command.
- So Cancel in Gripi wins until the gateway hears `starting`, and the command runs only if it didn't.

**Protocol:**
- CLI → gateway: `POST /pc/run` on the socket, with the form fields:
  - `session`, as for `/pc/ssh-agent`;
  - `argv`, repeated: 1 to 256 words of up to 4096 bytes of UTF-8 without NUL, the first not empty;
  - `cwd`, optional: an absolute path on the PC.

  It answers `200` with a `SudoResult` (`exit_code`, `stdout` and `stderr` in base64, `truncated`). Errors are as for `/pc/ssh-agent`: 400, 403, 404, 503, 504 when the command didn't start in time, and 502 for the desktop's own `error`. It is routed like `/pc/ssh-agent`, without connection pins.
- Gateway → desktop: `{"type": "run", "id", "session", "session_name", "argv", "cwd"}`, without `cwd` for the home folder. It needs a reply with that `id`.
- Desktop → gateway:
  - `POST /pc/link/reply` with `{"link", "id", "result": {"exit_code", "stdout", "stderr", "truncated"}}` or `{"link", "id", "error"}`. A reply's body may be up to 48 MiB, for 16 MiB per output in base64.
  - `POST /pc/link/status` may carry `"starting": true`, as above.
  - The open poll carries `"run": true`. A desktop app from before phase 4 drops a `run` message as malformed, and the gateway updates itself while the app doesn't, so the gateway refuses a command at once (503, "can't run commands; ask the user to update it") for a link without it.
- A poll carries at most 4 MiB of its messages' JSON, but a single message may have more: a command's words, which JSON may escape to 6 times their size, take up to about 6 MiB, and the desktop reads 8 MiB.
- The Push says "Approve on laptop: run a command", while the line shows the command.

**Desktop:**
- **Checks before the dialog**, which refuse at once with an error reply:
  - the message: `argv` of 1 to 256 strings of up to 4096 characters, the first not empty, and `cwd` a string if given;
  - the folder: absolute and an existing one, or the user's home folder without `cwd`;
  - the command: `argv[0]` with a slash is resolved against the folder, and otherwise on the app's `PATH`, skipping relative entries, to an executable file, as a shell would;
  - the command as shown at most 4096 characters, so the dialog stays readable.
- **The dialog** shows the session, the command with the resolved path and the other words quoted like `shellWords`, the folder and "As: you", with **Deny** (default and cancel) and **Run**. The approval turn's summary for Gripi's line is the command as shown.
- **The runner** spawns the resolved path with the other words, in the folder, with no shell, `stdio: ['ignore', 'pipe', 'pipe']`, `detached: true` and the app's own environment.
  - It keeps 16 MiB of each output and drops the rest, with `truncated`.
  - The exit code is the command's, or 128 plus the signal that killed it.
  - A cancel, a lost link or turning access off sends SIGTERM to the process group, and SIGKILL 5 s later, so that Git can remove its lock files first. Quitting, which waits at most 2 s, sends SIGKILL to the commands still running a second after SIGTERM.
  - When the result's reply fails, such as a large one on a slow uplink, it sends an error instead, as gripi pc run would otherwise wait for good. When that fails too, it drops the link, which ends the wait.
  - Quitting and turning access off track the process groups that got SIGTERM until their SIGKILL, also once the command's first process ended, as a process that it started in the background may still run.

**Workers:** both socket routes go into `workerRoutes`. `validOwnedSessionPath` checks the requesting session's placement with `reachesSession`, and routing then walks main's session keys, so a sub-session on a worker follows its parent on main.

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
