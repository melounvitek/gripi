# Gateway self-update

## Goal

Let an accessed gateway detect newer commits on `origin/master`, update a clean checkout, restart without depending on systemd, and force connected browsers to reload the complete updated page.

## Constraints

- Anyone who already has gateway access may update; there is no additional authorization step.
- Refuse updates from a dirty checkout, a checkout with local commits, a diverged checkout, or a branch other than `master`.
- Keep page rendering independent from network checks.
- The documented `bin/start` launcher provides restart supervision. Direct `bundle exec rackup` launches cannot self-restart.
- Roll back the Git checkout if dependency installation fails.
- Warn that restart interrupts active Pi work and close RPC children before restart.

## TDD rounds

1. Add integration coverage around temporary Git repositories, then implement update detection, safe fast-forwarding, dependency installation, and rollback.
2. Add launcher integration coverage, then implement the restart protocol in `bin/start` and graceful RPC cleanup.
3. Add request/UI coverage, then implement update status/action endpoints, asynchronous progress, sidebar controls, reconnect polling, and a forced full-page reload.
4. Document and validate the complete behavior, then run independent review and adopt useful simplifications.
