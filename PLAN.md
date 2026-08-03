# iPhone app plan

## Goal

Build a dependency-free SwiftUI iPhone client that mirrors the Electron shell around Gripi's existing responsive gateway UI. Pi and the gateway continue running on the configured remote machines.

## Scope

1. Add a tested Xcode project and persistent multi-gateway configuration.
2. Embed each gateway in an isolated persistent `WKWebView` with strict origin handling.
3. Add native clipboard, foreground notification, external-link, popup, and export/share integrations.
4. Add macOS CI, mobile regressions, and Mac/iPhone setup documentation.
5. Review the result independently and prepare a device-verification checklist.

Closed-app APNs delivery remains a separate milestone because its safe design depends on choosing a central relay or self-hosted Apple provider credentials. The first milestone will keep that boundary explicit and avoid unusable push registration code.

## Product decisions

- Minimum deployment target: iOS 17.
- Bundle identifier: `dev.gripi.ios`.
- No third-party runtime dependencies.
- The first launch asks for a gateway instead of defaulting to the phone's unusable localhost.
- User-entered HTTP and HTTPS gateway URLs are accepted. The gateway's existing production transport policy remains authoritative.
- Each gateway owns a stable, named `WKWebsiteDataStore`.
- Same-origin popup pages use an in-app sheet; external destinations open outside Gripi.
- HTML exports use the iOS share sheet.

## Verification

- Xcode unit tests for configuration, URL/origin policy, navigation decisions, filename handling, and notification payload validation.
- Node contract tests for the platform-neutral web bridge.
- Existing Go, frontend, Electron, and managed browser suites.
- macOS GitHub Actions simulator tests.
- Manual Mac simulator and physical-iPhone verification after the Linux implementation.
