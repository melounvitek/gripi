# AGENTS.md

## Planning files

This repo may use `TODO.md` for tracking follow-up work, rough ideas, and deferred tasks. When useful, suggest adding items there rather than losing them in chat.

This repo may also have a `PLAN.md`. If present, treat it as the active implementation plan, keep it in mind while working, and avoid drifting from it without discussion. For larger upcoming work, suggest creating or using `PLAN.md`.

When the current plan is completed, move the finished `PLAN.md` into the `plans/` folder.

## Git workflow

The primary branch for this repository is `master`.

## Local server

The dev server runs as the user systemd service `pi-web-gateway.service` on `100.103.198.74:4567`, logging to `/tmp/pi-web-gateway.log`.

Do not restart it unless explicitly asked; for code changes, tell the user a restart is needed. When restarting, use `systemctl --user restart pi-web-gateway.service` and verify with `systemctl --user status pi-web-gateway.service --no-pager` plus a curl check. For design-only changes (CSS/markup presentation tweaks), a restart is not needed; a browser refresh is enough.
