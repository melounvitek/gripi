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

By default, the gateway binds to `0.0.0.0`. To bind it only to a specific address, such as your Tailscale IP, pass the host as an argument:

```sh
mise run start 100.x.y.z
```

You can also configure the host or port with environment variables:

```sh
PI_GATEWAY_HOST=100.x.y.z mise run start
PI_GATEWAY_PORT=4568 mise run start
```

## Development server

```sh
mise run dev
```

## Dockerized runtime

The repository includes a portable Docker setup that runs both the gateway and the Pi CLI inside the container. The image includes Ruby, Node.js, Pi, mise, and common development tools. The only host directory exposed to Pi is the workspace you mount; Pi config, sessions, OAuth/API-key credentials, gateway config, and mise-installed runtimes persist in Docker volumes by default.

```sh
cp .env.example .env
mkdir -p workspace
# optionally edit PI_WORKSPACE in .env to an absolute path containing your codebases
```

`.env` may contain secrets such as provider API keys and `PI_GATEWAY_ADMIN_PASSWORD`; do not commit it. You can also leave provider keys blank and authorize Pi interactively:

```sh
docker compose build
docker compose run --rm pi pi
# inside Pi, run /login and choose your provider
```

Start the browser gateway:

```sh
docker compose up gateway
```

The gateway listens on <http://localhost:4567> by default and binds to `127.0.0.1`. Set `PI_GATEWAY_BIND=0.0.0.0` only when you want remote access. If `PI_GATEWAY_ADMIN_PASSWORD` is blank, the container generates one and stores it in the `gateway_config` volume. To set your own password, put it in `.env` before starting the gateway.

To expose existing codebases, set `PI_WORKSPACE` in `.env` to an absolute path:

```sh
PI_WORKSPACE=/home/alice/code
```

That host directory appears at the same path inside the container, so Pi sees `/home/alice/code/project-a` as `/home/alice/code/project-a`. If `PI_WORKSPACE` is not set, Compose falls back to `./workspace` mounted at `/work`.

Mise is available inside the container for project runtime setup. Trust and install tools from inside the mounted project as needed:

```sh
docker compose run --rm pi mise trust
docker compose run --rm pi mise install
docker compose run --rm pi mise exec -- <command>
```

Mise data, config, and cache are stored in Docker volumes, so installed runtimes survive container recreation without touching the host's mise installation.

For direct Pi use in the current repository without Compose, build the image and mount the current directory:

```sh
docker build -t pi-web-gateway:local .
docker run --rm -it \
  -v "$PWD:/work" \
  -v pi_web_gateway_pi_data:/home/piuser/.pi \
  -v pi_web_gateway_mise_data:/home/piuser/.local/share/mise \
  -v pi_web_gateway_mise_config:/home/piuser/.config/mise \
  -v pi_web_gateway_mise_cache:/home/piuser/.cache/mise \
  -w /work \
  pi-web-gateway:local pi
```

Advanced users can replace the named volumes with host mounts for `~/.pi` or gateway config, but doing so reduces isolation.

## Tests

```sh
mise run test
```
