# Security hardening plan

## Goal

Make Gripi safer to announce for trusted localhost/Tailscale usage by reducing browser-origin and accidental-exposure risks, without changing the core trust model: anyone who can use the gateway can drive Pi on the gateway machine.

## Non-goals

- Do not make Gripi safe for public internet exposure.
- Do not provide OS-level isolation between multi-user workspaces.
- Do not redesign Pi-owned session/runtime behavior.
- Do not add heavyweight auth infrastructure unless the smaller mitigations prove insufficient.

## Threat model

### In scope

- Unauthenticated clients on the same LAN/VPN trying to access a gateway with browser approval enabled.
- Malicious websites opened in an approved browser trying to trigger gateway actions.
- Accidental broad binding, especially `0.0.0.0`.
- Low-effort DoS or side effects from cross-site navigations.

### Out of scope / documented risk

- Approved users intentionally asking Pi to run dangerous commands.
- Compromised approved browser/device.
- Public internet exposure.
- Untrusted multi-user tenants.

## Implementation checklist

### 1. Add same-origin protection for unsafe requests

- [x] Add a small Sinatra helper/middleware that protects unsafe HTTP methods (`POST`, `PUT`, `PATCH`, `DELETE`).
- [x] Allow requests when `Origin` is absent for compatibility, but reject them when a present `Referer` is not same-origin.
- [x] Reject requests with an `Origin` whose scheme/host/port does not match the current request base URL.
- [x] Use `Sec-Fetch-Site` as an additional signal where available; reject `cross-site` unsafe requests.
- [x] Ensure Electron, browser UI fetches, and plain HTML forms keep working.
- [x] Return a clear `403` response for rejected unsafe requests.

### 2. Decide whether CSRF tokens are needed

- [x] Decide whether Origin/Sec-Fetch-Site protection is sufficient for this app. Decision: sufficient for the current trusted localhost/Tailscale alpha scope; defer CSRF tokens unless a concrete bypass or broader deployment model appears.

### 3. Reduce side effects from GET routes

- [x] Audit all GET routes and classify them as cheap read-only, expensive read-only, or side-effectful.
  - Cheap/static/status reads: PWA/icon routes, browser/workspace access status and pending lists, attachments, older conversation windows, cwd validation.
  - Filesystem/UI reads: `/`, `/sidebar`, `/new_session_modal`, `/session_fragment`, `/sessions/browse_cwd`.
  - Existing-client reads: `/events`, `/status`.
  - RPC-starting reads: `/sessions/model_settings`, `/sessions/fork_messages`, `/sessions/tree_entries`, `/commands`.
  - External side effect: `/gateway-update` previously ran `git fetch` through the status check.
- [x] Avoid starting Pi RPC clients from GET routes where reasonable. Decision: defer RPC-backed UI endpoint changes because they are authenticated UI reads and changing them is more likely to break normal flows.
- [x] Consider making `/gateway-update` status use cached/freshness-limited data instead of `git fetch` on every GET.
- [x] Keep UI behavior unchanged or explicitly document any refresh button/polling changes.

### 4. Safer default bind address

- [x] Change `bin/start` default host from `0.0.0.0` to `127.0.0.1`, or require an explicit env var to bind all interfaces.
- [x] Update README install/start examples if needed.
- [x] Update `docs/configuration.md` and `docs/examples.md` to match the chosen behavior.
- [x] Preserve intentional Tailscale direct binding via `GRIPI_HOST=100.x.y.z`.

### 5. Strengthen auth/exposure docs

- [x] Clarify that browser approval should stay enabled for any reachable gateway.
- [x] Warn that `GRIPI_BROWSER_AUTH_DISABLED=1` is only for fully trusted, already-isolated environments.
- [x] Clarify that multi-user mode is not a sandbox and approved users can start Pi in arbitrary accessible directories.
- [x] Consider adding a short `Security` section to README.

### 6. Capture optional hardening follow-ups

- [x] Move lower-priority hardening ideas into the future follow-ups section below.

## Proposed TDD rounds

### Round 1 — unsafe request protection

- [x] Add request specs proving same-origin unsafe requests still work.
- [x] Add request specs proving cross-origin unsafe requests are rejected by the global before-filter for representative endpoints:
  - [x] `/prompt` (covered by global unsafe-request filter; dedicated endpoint test deferred as unnecessary for this round)
  - [x] `/abort` (covered by global unsafe-request filter; dedicated endpoint test deferred as unnecessary for this round)
  - [x] `/gateway-update`
  - [x] `/browser-access/approve`
  - [x] `/workspace-access/approve`
- [x] Implement the smallest shared protection layer.
- [x] Run the full Ruby test suite. Current run reaches the suite but fails in existing `DemoTest#test_demo_embeds_the_exact_production_stylesheet`, unrelated to this change.

### Round 2 — default host hardening

- [x] Add/update script tests for `bin/start` default host.
- [x] Change launcher behavior.
- [x] Update docs.
- [x] Run relevant script/docs tests and full Ruby suite. Current full suite still fails only in existing `DemoTest#test_demo_embeds_the_exact_production_stylesheet`, unrelated to this change.

### Round 3 — GET side-effect reduction

- [x] Add regression tests around the selected GET behavior.
- [x] Refactor only endpoints where there is a clear low-risk improvement.
- [x] Verify sidebar/update/model/settings UI paths still work.
- [x] Run full Ruby suite and targeted JS/Electron checks if touched. Targeted Ruby and JS controller tests pass; full suite still has the existing unrelated DemoTest stylesheet mismatch.

### Round 4 — docs and final review

- [x] Update README/configuration/examples security wording.
- [x] Run all relevant tests:
  - [x] `mise run test` (runs but still fails only in existing `DemoTest#test_demo_embeds_the_exact_production_stylesheet` stylesheet mismatch)
  - N/A: `mise run desktop-check` (Electron files were not changed)
- [x] Run an independent subagent review focused on simplification and missed security gaps.
- [x] Adopt useful review suggestions.
- [x] Rerun review if changes are made after it.

## Future follow-ups

These are intentionally deferred from this branch because they are lower priority for the trusted localhost/Tailscale alpha scope, need broader compatibility testing, or risk changing normal UI behavior.

- Consider adding full CSRF tokens if Origin/Sec-Fetch-Site protection proves insufficient or the deployment model expands.
- Consider converting RPC-backed GET endpoints (`/sessions/model_settings`, `/sessions/fork_messages`, `/sessions/tree_entries`, `/commands`) only if there is a low-risk UI-compatible design.
- Consider `SameSite=Strict` for gateway cookies and verify mobile/PWA/Electron flows.
- Consider explicit `GRIPI_PERMITTED_HOSTS` guidance for reachable production deployments.
- Consider honoring `X-Forwarded-*` origin headers only behind explicitly configured trusted proxies.
- Consider rate-limiting approval/password attempts if exposed beyond localhost.
- Consider allow-listed cwd roots for multi-user mode.
- Consider file permissions for JSON state files that contain approved browser/workspace tokens.

## Validation notes

- Manual browser verification may be useful after implementation, especially for:
  - browser approval flow
  - workspace approval flow
  - prompt submit/abort
  - gateway update control
  - desktop app connection
- Chromium-based verification could be used if desired, but should be requested explicitly before running.

## Resolved questions

- Unsafe requests with no `Origin` are accepted for compatibility; browser-origin attacks are mitigated by rejecting cross-origin `Origin`, cross-origin `Referer`, and `Sec-Fetch-Site: cross-site` requests.
- `SameSite=Strict` is deferred until after mobile Home Screen and Electron compatibility testing.
- `/gateway-update` no longer fetches on GET; fresh update checks use protected POST requests.
