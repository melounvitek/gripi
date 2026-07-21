# Retire idle Pi clients periodically

## Goal

Reclaim memory from settled Pi RPC clients without relying on browser requests, while preserving active work, persisted Pi sessions, and native Pi workflows.

## Behavior

- Sweep idle clients every 30 seconds.
- Retire clients five minutes after their latest use or settlement.
- Browser polling does not renew the idle timer.
- Busy agent turns, compaction, gateway bash, and active request leases remain protected.
- Reconcile persisted session state before retirement and remove expired pending-session metadata.
- Start maintenance when Puma boots and stop it cleanly with Puma.
- Preserve environment overrides for the timeout and sweep interval.

## TDD rounds

1. Add a standard-library periodic maintenance object with deterministic lifecycle, failure recovery, duplicate-start protection, and allocation/RSS stability coverage.
2. Move idle-client cleanup from HTTP requests into one application-level sweep, change the default timeout to five minutes, and preserve synchronization and pending-session cleanup.
3. Integrate maintenance with Puma lifecycle hooks and verify with managed-Puma/Chromium coverage that polling does not keep an idle Pi process alive and a later operation starts a fresh client.
4. Run focused and full suites, benchmark repeated sweeps, obtain independent review, archive this plan, merge, and push. Do not restart `gripi.service` without explicit approval.
