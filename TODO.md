# TODO

## Native iPhone notifications

Choose the APNs delivery model before implementing closed-app notifications:

- Central Gripi relay: suitable for a generally distributed App Store build, but introduces hosted infrastructure and limited notification metadata processing.
- Direct self-hosted APNs: preserves self-hosting, but requires every builder to configure an Apple Developer provider key on each gateway.

After choosing, cover authenticated device registration and removal, token rotation, gateway ownership, completion delivery, test notifications, multi-gateway deep links, and payload minimization.
