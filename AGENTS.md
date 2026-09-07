# AGENTS.md

## Touch interactions

Touch controls must activate on the first tap. Avoid sticky-hover behavior that requires a second tap, and cover custom touch interactions with regression tests.

## Planning files

This repo may use `TODO.md` for tracking follow-up work, rough ideas, and deferred tasks. When useful, suggest adding items there rather than losing them in chat.

This repo may also have a `PLAN.md`. If present, treat it as the active implementation plan, keep it in mind while working, and avoid drifting from it without discussion. For larger upcoming work, suggest creating or using `PLAN.md`.

When the current plan is completed, delete the finished `PLAN.md`.

## Native Pi alignment

Keep gateway features aligned with native Pi CLI behavior. Preserve Pi-owned data formats and workflows, and store gateway-only metadata separately when needed. If a web-specific behavior must diverge from Pi CLI behavior, call out the tradeoff before implementing.

## UI rendering

For changes affecting conversation/message rendering, check both server-rendered history and live-appended event rendering. Many message shapes are rendered twice: once by Go templates for page load, and once by JavaScript for live events. A fix that looks correct after a reload may still need a matching live-renderer update.

## Local server

The dev server runs as the user systemd service `gripi.service`, logging to `/tmp/gripi.log`.

Do not restart it unless explicitly asked. Go code, HTML templates, and frontend assets (including CSS and JavaScript) are compiled into the gateway binary. Changes to these files require a rebuild and service restart, followed by a browser refresh; presentation-only changes are not an exception.

Before restarting, verify whether the launcher rebuilds changed files. The current `bin/start` only builds from source when `tmp/gripi` is missing; otherwise it reuses the binary or installs a staged update. Rebuild the current code before restarting an existing binary. After restarting, verify that `gripi.service` is running and the gateway responds.
