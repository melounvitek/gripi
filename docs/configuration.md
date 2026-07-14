# Configuration

GRIPi reads most local settings from `~/.config/gripi/env`.

## Server address

Pass the server host or port when starting the gateway:

```sh
GRIPI_HOST=127.0.0.1 mise run start
GRIPI_HOST=100.x.y.z GRIPI_PORT=4568 mise run start
```

`GRIPI_HOST` and `GRIPI_PORT` choose where the gateway server listens. Set them in the command environment, not only in `~/.config/gripi/env`.

## Self-updates

The gateway checks `origin/master` after the page loads and shows a sidebar control when newer commits are available. Anyone who can access the gateway can trigger an update. Updating interrupts active Pi work.

An update is accepted only when the checkout:

- is on `master`
- has no tracked or untracked changes
- has no local commits
- can be fast-forwarded to `origin/master`

The gateway fetches and fast-forwards the checkout, runs `bundle install`, then requests a restart. If dependency installation fails, it resets tracked files to the previous commit and keeps the existing server running. It does not automatically delete newly generated untracked files. The failure remains in the sidebar so any connected browser can see it and retry. If the rollback itself fails, the gateway requires manual checkout recovery instead of offering an unsafe automatic retry.

Automatic restart requires the gateway to be launched through `mise run start` or `bin/start`. The launcher supervises the Rack process itself, so this works with or without systemd. A direct `bundle exec rackup` process can update its checkout but cannot start a replacement server after it exits.

After the replacement server responds, the initiating page and sibling tabs navigate to a cache-busted copy of their current URL. Other open clients detect the replacement periodically or as soon as they regain focus. This performs a complete reload while preserving the selected session and other query state.

The restart marker defaults to `~/.pi/gripi/restart-request`. Override it only when needed, and set the override in the launcher process environment—not only in the gateway env file loaded by Ruby:

```sh
GRIPI_RESTART_PATH=/path/to/restart-request mise run start
```

## Common options

```sh
GRIPI_BROWSER_AUTH_DISABLED=1
GRIPI_MULTI_USER_MODE=1
```

`GRIPI_BROWSER_AUTH_DISABLED=1` skips browser approval for trusted private URLs.

`GRIPI_MULTI_USER_MODE=1` asks users for a personal session key before showing sessions. The same key on another browser shows the same sessions. This separates gateway session visibility for trusted users, but it is not OS-level process, filesystem, or credential isolation.

## Custom Pi runtime

If Pi needs a different Node runtime than the one selected by mise, set both:

```sh
GRIPI_NODE=/path/to/node
GRIPI_PI=/path/to/pi
```

## Pinned session directories

Add pinned session directories to `~/.config/gripi/pinned-dirs` to keep them available in the New Session dialog:

```txt
/home/alice/projects/gripi
/home/alice/projects/another-project
```

One directory per line. Blank lines and `#` comments are ignored.
