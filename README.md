<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="branding/gripi-wordmark-dark.svg">
    <img src="branding/gripi-wordmark.svg" alt="" width="350">
  </picture>
</p>

**Gripi is a desktop and web portal for [Pi](https://pi.dev/), powered by a self-hosted gateway.** Run the gateway on a development machine or home server with Pi CLI installed, then use Pi from the desktop app or any web browser. Locally, or over an encrypted private network.

**Pi stays Pi.** Gripi does not alter Pi’s system prompt, patch Pi, install extensions, rewrite sessions, or change Pi-owned configuration. It is a gateway and UI layer for accessing the Pi environment you already run.

<p align="center">
  <strong><a href="https://gripi.w10.cz/">Try the live interactive demo →</a></strong><br>
  <sub>No installation required. Responses and backend actions are simulated.</sub>
</p>

<a href="docs/images/gripi-architecture.svg"><img alt="Desktop, browser, and mobile clients connect over a local network or VPN to the Gripi gateway, which runs Pi with access to the gateway machine's projects, sessions, and credentials" src="docs/images/gripi-architecture.svg" /></a>

> The project is a fully vibe-coded alpha version ATM. Initially, it was supposed to be a quick proof of concept, but I ended up using it for my daily work and I actually like it. So please, feel free to try it; but expect some rough edges, missing features, and behavior that may change. Happy to look at any feedback (use Github issues)!

## Install

Requirements:

- [mise](https://mise.jdx.dev/)
- [Pi CLI](https://pi.dev/) available on `PATH`

```sh
git clone https://github.com/melounvitek/gripi.git
cd gripi
mise install
mise run setup
```

Setup stores an admin password in `~/.config/gripi/env` and prints it. Start the gateway:

```sh
mise run start
```

By default, the gateway listens only on `127.0.0.1`. Open <http://localhost:4567> and use the admin password to approve your browser.

### Desktop app

The recommended desktop app is available on macOS and Linux. Installing it requires Node.js 22.12 or newer and, on Linux, FUSE 2 (`fuse2` on Arch Linux).

```sh
mise run desktop-install
```

The desktop app connects to the running gateway and can store and switch between multiple gateways.

<img width="1468" height="930" alt="Screenshot 2026-07-15 at 19 23 37" src="https://github.com/user-attachments/assets/194b8d2a-5e1d-43c5-aae7-a8092e73b6f4" />


There is no mobile app, but on iPhone, adding the gateway to the Home Screen with Apple's [Open as Web App](https://support.apple.com/guide/iphone/open-as-web-app-iphea86e5236/ios) flow works nicely:

<img width="804" height="362" alt="image" src="https://github.com/user-attachments/assets/37ab55d7-7b34-4cce-932e-566a6d415041" />

<img width="360" alt="Gripi running as an iPhone web app" src="docs/images/gripi-mobile-screenshot.png" />

## Usage modes

By default, the gateway runs in single-user mode and shows all Pi sessions to one trusted user. Optional multi-user mode gives each user a private user token and shows only the sessions associated with that token.

Multi-user mode is intended for trusted users. It does not provide OS-level process, filesystem, or credential isolation, and settings such as the selected model and thinking effort are currently shared between users. See [configuration](docs/configuration.md#multi-user-mode) to enable it.

## Security and remote access

Do not expose the gateway directly to the public internet. Anyone who can use it can start Pi processes with the gateway machine's filesystem and credentials.

Keep access approval enabled for any gateway reachable from another device: browser approval in single-user mode, or user-token approval in multi-user mode. Use HTTPS or an encrypted VPN such as Tailscale for remote access; ordinary LAN or Wi-Fi HTTP can expose passwords and access cookies. Production mode rejects remote plaintext HTTP unless `GRIPI_ALLOW_INSECURE_REMOTE_HTTP=1` explicitly allows transport already encrypted by a private VPN. Only disable approval when access is already limited to trusted devices and users. Multi-user mode separates session visibility for trusted users; it is not a sandbox and does not isolate filesystem, process, or credential access.

- [Example local and remote setups](docs/examples.md)
- [Configuration options](docs/configuration.md)

## Pi compatibility

Gripi is intentionally thin around Pi. It uses Pi’s existing runtime, sessions, tools, models, and configuration instead of replacing them with Gripi-specific behavior. Gateway-only metadata is stored separately when needed.

Gripi supports RPC-compatible extension UI such as select, confirm, input, editor, notify, status, title, and editor-prefill requests. If a workflow depends on Pi’s native terminal UI, custom TUI components, terminal keybindings, or `ctx.mode === "tui"`, use Pi CLI directly.

## Optional Pi setup

If you do not already have a session-naming workflow, consider installing [`@furbyhaxx/pi-session-naming`](https://github.com/furbyhaxx/pi-session-naming):

```sh
pi install npm:@furbyhaxx/pi-session-naming
```

## Note

This project is written in Ruby, because I am a Ruby developer trying full vibe-coding for the first time, and I expected I might need to jump in. It turned out that was not needed, so I have mostly stayed out of the generated code -- so please, do not treat it as a sample of my usual Ruby style. It very likely is not :-).

## Development

```sh
mise run dev
mise run test
```
