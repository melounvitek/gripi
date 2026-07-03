# Pi Web Gateway

Browser UI for local Pi sessions.

<img width="1470" height="956" alt="image" src="https://github.com/user-attachments/assets/09b55035-b68a-46d7-b1ce-24697d8e3412" />

I'm scared to see the code (this my test of true vibe-coding), but it works nicely!

## Features

- Browse, resume, and start Pi sessions from the browser
- Use locally or over a private VPN such as Tailscale
- Supports many native Pi slash commands, including `/tree`, `/compact`, and custom skills

## Status and security

Pi Web Gateway provides a browser UI for working with Pi CLI sessions. It can run purely on your local machine, or on a standalone machine reachable over a private VPN such as Tailscale, which is the preferred way to use it remotely.

Pi Web Gateway tries to stay close to native Pi CLI behavior: Pi-owned session data and workflows are preserved, with gateway-only metadata stored separately where needed.

The gateway has basic browser approval and admin-password protection, but it is still intended only for trusted networks. Do not expose it directly to the public internet. Approved browsers can view sessions and start Pi processes with the same local filesystem, repository, and credential access as the gateway process.

For single-user deployments on a trusted private URL, set `PI_BROWSER_AUTH_DISABLED=1` to skip browser approval entirely. Anyone who can open the gateway URL can use it. In multi-user mode, this only disables browser approval; each user still needs an approved personal user token.

## Requirements

- [mise](https://mise.jdx.dev/)
- [Pi CLI](https://pi.dev/) available on `PATH`

## Setup

```sh
mise trust
mise install
mise run setup
```

The setup task installs Ruby dependencies and creates a local gateway config file at `~/.config/pi-web-gateway/env` if needed. When `PI_GATEWAY_ADMIN_PASSWORD` is missing, setup generates a random admin password there and prints it once. Change the gateway admin password by editing that file. The admin password is not required when `PI_BROWSER_AUTH_DISABLED=1` is set outside multi-user mode.

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

## Pinned session directories

Add directories to `~/.config/pi-web-gateway/pinned-dirs` to keep them available in the New Session dialog, even when they do not currently have sessions:

```txt
/home/alice/projects/pi-web-gateway
/home/alice/projects/another-project
```

Use one directory per line. Blank lines and `#` comments are ignored. Only existing readable directories are shown. Set `PI_SESSION_CWDS_PATH` to use a different file.

## Shared gateway session keys

Set `PI_MULTI_USER_MODE=1` to ask users for a personal session key before showing sessions. The key selects a private session list: the same key on another browser shows the same sessions. Existing sessions without an owner are hidden while this mode is on, but they reappear when the mode is off.

The gateway generates a stable workspace secret at `~/.pi/web-gateway/workspace-secret` by default. Override `PI_WORKSPACE_SECRET_PATH` or `PI_WORKSPACE_OWNERSHIP_PATH` if you need different storage locations.

This mode separates gateway session visibility for trusted users. It is not OS-level process, filesystem, or credential isolation.

## Note

This project is written in Ruby, because I am a Ruby developer, and I expected I might need to jump in while trying full vibe-coding for the first time. I have mostly stayed out of the generated code, so please do not treat it as a sample of my usual Ruby style, it probably is not :-).

## Development server

```sh
mise run dev
```

## Tests

```sh
mise run test
```
