# AGENTS.md

## Planning files

This repo may use `TODO.md` for tracking follow-up work, rough ideas, and deferred tasks. When useful, suggest adding items there rather than losing them in chat.

This repo may also have a `PLAN.md`. If present, treat it as the active implementation plan, keep it in mind while working, and avoid drifting from it without discussion. For larger upcoming work, suggest creating or using `PLAN.md`.

When the current plan is completed, move the finished `PLAN.md` into the `plans/` folder.

## Git workflow

The primary branch for this repository is `master`.

## Native Pi alignment

Keep gateway features aligned with native Pi CLI behavior. Preserve Pi-owned data formats and workflows, and store gateway-only metadata separately when needed. If a web-specific behavior must diverge from Pi CLI behavior, call out the tradeoff before implementing.

## UI rendering

For changes affecting conversation/message rendering, check both server-rendered history and live-appended event rendering. Many message shapes are rendered twice: once by Ruby/ERB for page load, and once by JavaScript for live events. A fix that looks correct after a reload may still need a matching live-renderer update.

## Local server

The dev server runs as the user systemd service `pi-web-gateway.service`, logging to `/tmp/pi-web-gateway.log`.

Do not restart it unless explicitly asked; for code changes, tell the user a restart is needed. When restarting, use `systemctl --user restart pi-web-gateway.service` and verify with `systemctl --user status pi-web-gateway.service --no-pager` plus a curl check. For design-only changes (CSS/markup presentation tweaks), a restart is not needed; a browser refresh is enough.
