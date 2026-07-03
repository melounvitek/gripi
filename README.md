# Pi Web Gateway

Browser UI for local Pi sessions.

<img width="1470" height="956" alt="image" src="https://github.com/user-attachments/assets/3aa0d8f3-265b-4de0-ac65-b91f7af70a2f" />


## Requirements

- [mise](https://mise.jdx.dev/)
- Pi CLI available on `PATH`

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
