# Pi Web Gateway

Browser UI for local Pi sessions.

## Requirements

- [mise](https://mise.jdx.dev/)
- Pi CLI available on `PATH`

## Setup

```sh
mise trust
mise install
mise run setup
```

The setup task installs Ruby dependencies and creates a local gateway config file at `~/.config/pi-web-gateway/env` if needed. When `PI_GATEWAY_ADMIN_PASSWORD` is missing, setup generates a random admin password there and prints the file path. You can change the gateway admin password by editing that file.

## Run the gateway

```sh
mise run start
```

The gateway listens on <http://localhost:4567>.

## Development server

```sh
mise run dev
```

## Tests

```sh
mise run test
```
