# Pi Web Gateway

Browser UI for local Pi sessions.

<img width="1470" height="956" alt="image" src="https://github.com/user-attachments/assets/09b55035-b68a-46d7-b1ce-24697d8e3412" />

## Features

- Browse, resume, and start Pi sessions from the browser
- Use locally or over a private VPN such as Tailscale
- Supports many native Pi slash commands, including `/tree`, `/compact`, and custom skills

## Status and security

Pi Web Gateway provides a browser UI for working with Pi CLI sessions. It can run purely on your local machine, or on a standalone machine reachable over a private VPN such as Tailscale, which is the preferred way to use it remotely.

Pi Web Gateway tries to stay close to native Pi CLI behavior: Pi-owned session data and workflows are preserved, with gateway-only metadata stored separately where needed.

The gateway has basic browser approval and admin-password protection, but it is still intended only for trusted networks. Do not expose it directly to the public internet. Approved browsers can view sessions and start Pi processes with the same local filesystem, repository, and credential access as the gateway process.

## Requirements

- [mise](https://mise.jdx.dev/)
- [Pi CLI](https://pi.dev/) available on `PATH`

## Setup

```sh
mise trust
mise install
mise run setup
```

The setup task installs Ruby dependencies and creates a local gateway config file at `~/.config/pi-web-gateway/env` if needed. When `PI_GATEWAY_ADMIN_PASSWORD` is missing, setup generates a random admin password there and prints it once. Change the gateway admin password by editing that file.

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

## Pinned session directories

Add directories to `~/.config/pi-web-gateway/pinned-dirs` to keep them available in the New Session dialog, even when they do not currently have sessions:

```txt
/home/vitek/Work/pi-web-gateway
/home/vitek/Work/another-project
```

Use one directory per line. Blank lines and `#` comments are ignored. Only existing readable directories are shown. Set `PI_SESSION_CWDS_PATH` to use a different file.

## Shared gateway session keys

Set `PI_MULTI_USER_MODE=1` to ask approved browsers for a personal session key before showing sessions. The key selects a private session list: the same key on another approved browser shows the same sessions. Existing sessions without an owner are hidden while this mode is on, but they reappear when the mode is off.

The gateway generates a stable workspace secret at `~/.pi/web-gateway/workspace-secret` by default. Override `PI_WORKSPACE_SECRET_PATH` or `PI_WORKSPACE_OWNERSHIP_PATH` if you need different storage locations.

This mode separates gateway session visibility for trusted users. It is not OS-level process, filesystem, or credential isolation.

## Development server

```sh
mise run dev
```

## Tests

```sh
mise run test
```
