# Web Push notifications

## Goal

Deliver completed assistant-reply notifications to installed iOS PWAs while Gripi is closed, without an Apple developer account. Preserve the existing reply preview and focused-session suppression behavior, fan out to every subscribed device for the owning user, and leave Electron notifications unchanged.

## TDD rounds

- [x] Round 1: Persistent VAPID identity, subscription store, and Web Push delivery foundation.
- [x] Round 2: Authenticated subscription and test-delivery API with single-user and multi-user isolation.
- [ ] Round 3: First-tap browser subscription lifecycle and notification-test flow.
- [ ] Round 4: Server-side completed-assistant event observation and owner-targeted delivery.
- [ ] Round 5: Service-worker push display, focused-session suppression, documentation, full verification, and independent review.

## Boundaries

- Push only completed assistant replies with final, non-commentary text.
- No access-request or intermediate-event pushes.
- No durable notification outbox; delivery and bounded transient retries are best effort.
- Only Pi RPC processes managed by this Gripi gateway are observed.
- Requires iOS/iPadOS 16.4+, an installed Home Screen app, HTTPS, permission, and outbound gateway internet access.
- Store gateway-only Web Push metadata separately from Pi-owned files.
