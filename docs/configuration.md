# Configuration

Pi Web Gateway reads most local settings from `~/.config/pi-web-gateway/env`.

## Server address

Pass the server host or port when starting the gateway:

```sh
PI_GATEWAY_HOST=127.0.0.1 mise run start
PI_GATEWAY_HOST=100.x.y.z PI_GATEWAY_PORT=4568 mise run start
```

`PI_GATEWAY_HOST` and `PI_GATEWAY_PORT` choose where the gateway server listens. Set them in the command environment, not only in `~/.config/pi-web-gateway/env`.

## Common options

```sh
PI_BROWSER_AUTH_DISABLED=1
PI_MULTI_USER_MODE=1
```

`PI_BROWSER_AUTH_DISABLED=1` skips browser approval for trusted private URLs.

`PI_MULTI_USER_MODE=1` asks users for a personal session key before showing sessions. The same key on another browser shows the same sessions. This separates gateway session visibility for trusted users, but it is not OS-level process, filesystem, or credential isolation.

## Custom Pi runtime

If Pi needs a different Node runtime than the one selected by mise, set both:

```sh
PI_GATEWAY_NODE=/path/to/node
PI_GATEWAY_PI=/path/to/pi
```

## Pinned session directories

Add pinned session directories to `~/.config/pi-web-gateway/pinned-dirs` to keep them available in the New Session dialog:

```txt
/home/alice/projects/pi-web-gateway
/home/alice/projects/another-project
```

One directory per line. Blank lines and `#` comments are ignored.
