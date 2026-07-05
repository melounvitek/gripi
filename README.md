# Pi Web Gateway

Browser UI for local Pi sessions.

<img width="1470" height="956" alt="Pi Web Gateway desktop screenshot" src="https://github.com/user-attachments/assets/09b55035-b68a-46d7-b1ce-24697d8e3412" />

<img width="360" alt="Pi Web Gateway mobile screenshot" src="docs/images/mobile-screenshot.png" />

I'm scared to look into the code (the project is my attempt to try real vibe-coding), but it works nicely!

## Features

- Browse, resume, and start Pi sessions from the browser
- Use locally or over a private VPN such as Tailscale
- Supports many native Pi slash commands, including `/tree`, `/compact`, and custom skills

## Status and security

Pi Web Gateway provides a browser UI for working with Pi CLI sessions. It can run purely on your local machine, or on a standalone machine reachable over a private VPN such as Tailscale, which is the preferred way to use it remotely.

Pi Web Gateway tries to stay close to native Pi CLI behavior: Pi-owned session data and workflows are preserved, with gateway-only metadata stored separately where needed.

The gateway has basic browser approval and admin-password protection, but it is still intended only for trusted networks. Do not expose it directly to the public internet. Approved browsers can view sessions and start Pi processes with the same local filesystem, repository, and credential access as the gateway process.

For deployments on a trusted private URL, set `PI_BROWSER_AUTH_DISABLED=1` to skip browser approval entirely. Anyone who can open the gateway URL can use it. In multi-user mode, users still need a personal user token, but new tokens are accepted immediately without approval.

## Requirements

- [mise](https://mise.jdx.dev/)
- [Pi CLI](https://pi.dev/) available on `PATH`

## Setup

```sh
git clone https://github.com/melounvitek/pi-web-gateway.git
cd pi-web-gateway
mise trust
mise install
mise run setup
```

The setup task installs Ruby dependencies and creates a local gateway config file at `~/.config/pi-web-gateway/env` if needed. When `PI_GATEWAY_ADMIN_PASSWORD` is missing, setup generates a random admin password there and prints it once. Change the gateway admin password by editing that file. The admin password is not required when `PI_BROWSER_AUTH_DISABLED=1` is set.

## Optional Pi setup

Pi Web Gateway uses native Pi session names when available. If you do not already have your own session-naming workflow, consider installing the [`@furbyhaxx/pi-session-naming`](https://github.com/furbyhaxx/pi-session-naming) Pi package:

```sh
pi install npm:@furbyhaxx/pi-session-naming
```

## Run the gateway

```sh
mise run start
```

The gateway listens on <http://localhost:4567>.

By default, the gateway binds to `0.0.0.0`. To bind it only to a specific address, such as your Tailscale IP, pass the host as an argument:

```sh
mise run start 100.x.y.z
```

You can also configure the host or port with environment variables:

```sh
PI_GATEWAY_HOST=100.x.y.z mise run start
PI_GATEWAY_PORT=4568 mise run start
```

If Pi must run with a different Node runtime than the project-local one selected by mise, set both paths in `~/.config/pi-web-gateway/env`:

```sh
PI_GATEWAY_NODE=/path/to/node
PI_GATEWAY_PI=/path/to/pi
```

When these are set, the gateway starts Pi as `$PI_GATEWAY_NODE $PI_GATEWAY_PI`. Set both variables together, or leave both unset to run `pi` from `PATH`.

## App-like use

Pi Web Gateway works well as an installed web app:

- On iPhone or iPad, add it to your Home Screen and open it as a web app: <https://support.apple.com/guide/iphone/open-as-web-app-iphea86e5236/ios>
- On Mac or Linux, install it as a Chrome web app: <https://support.google.com/chrome/answer/9658361?hl=en&co=GENIE.Platform%3DDesktop>

### Electron desktop shell

This repository also includes a thin Electron shell. It does not start or bundle the gateway server; start the server separately first:

```sh
mise run start
```

Then, in another terminal, run the desktop shell:

```sh
npm install
mise run desktop
```

The shell opens <http://localhost:4567/> by default. If that URL is unavailable, the desktop shell lets you save another gateway URL, such as a Tailscale/private-network address. Saved gateways open as tabs with isolated Electron sessions, so browser approval, cookies, storage, and service workers are separate for each gateway.

Use **File → Add Gateway…** to add another gateway, and **File → Remove Current Gateway…** to remove the active gateway. The tab bar stays hidden while only one gateway is configured.

You can also point the active gateway at a different URL for one launch with either option:

```sh
PI_GATEWAY_DESKTOP_URL=https://pi-gateway.example.internal mise run desktop
npm run desktop -- --gateway-url=http://100.x.y.z:4567
```

Command-line and environment URLs take precedence over the saved URL.

Build distributable Electron packages with electron-builder after installing npm dependencies:

```sh
npm ci
```

On Linux, build an AppImage artifact under `dist/`:

```sh
mise run desktop-dist-linux
```

On macOS, build dmg and zip artifacts under `dist/`:

```sh
mise run desktop-dist-mac
```

To build and install or update the desktop shell for the current user:

```sh
mise run desktop-install
```

On macOS, this installs the app to `~/Applications` so Spotlight can find it. On Linux, this installs the AppImage under `${XDG_DATA_HOME:-~/.local/share}/pi-web-gateway` and registers a desktop launcher under `${XDG_DATA_HOME:-~/.local/share}/applications`.

macOS packages must be built on macOS. Unsigned macOS builds are useful for local testing, but distributing them to other users may trigger Gatekeeper warnings unless they are signed and notarized with an Apple Developer ID.

## Pinned session directories

Add directories to `~/.config/pi-web-gateway/pinned-dirs` to keep them available in the New Session dialog, even when they do not currently have sessions:

```txt
/home/alice/projects/pi-web-gateway
/home/alice/projects/another-project
```

Use one directory per line. Blank lines and `#` comments are ignored. Only existing readable directories are shown. Set `PI_SESSION_CWDS_PATH` to use a different file.

## Shared gateway session keys

Set `PI_MULTI_USER_MODE=1` to ask users for a personal session key before showing sessions. The key selects a private session list: the same key on another browser shows the same sessions. Existing sessions without an owner are hidden while this mode is on, but they reappear when the mode is off. With `PI_BROWSER_AUTH_DISABLED=1`, new keys are accepted immediately; otherwise, an approved user must approve each new key.

The gateway generates a stable workspace secret at `~/.pi/web-gateway/workspace-secret` by default. Override `PI_WORKSPACE_SECRET_PATH` or `PI_WORKSPACE_OWNERSHIP_PATH` if you need different storage locations.

This mode separates gateway session visibility for trusted users. It is not OS-level process, filesystem, or credential isolation.

## Note

This project is written in Ruby, because I am a Ruby developer trying full vibe-coding for the first time, and I expected I might need to jump in. It turned out that was not needed, so I have mostly stayed out of the generated code -- so please, do not treat it as a sample of my usual Ruby style. It very likely is not :-).


## Development server

```sh
mise run dev
```

## Tests

```sh
mise run test
```
