# iPhone client

The native iPhone client is a thin SwiftUI shell around the same responsive gateway UI used in a browser and the Electron app. It does not run Pi or the Gripi gateway on the phone. Each configured gateway must already be running on another machine that the iPhone can reach.

## Requirements

- iOS 17 or newer
- A Mac with a current Xcode release
- An iPhone signing identity selected in Xcode
- A reachable Gripi gateway, preferably over HTTPS or an encrypted private network such as Tailscale

`localhost` on an iPhone refers to the phone, not the Mac or server running Gripi.

## Install from Xcode

1. Open `ios/Gripi.xcodeproj`.
2. Select the `Gripi` target and its **Signing & Capabilities** tab.
3. Select your development team.
4. If Xcode reports that `dev.gripi.ios` is unavailable, replace it with a bundle identifier unique to your Apple developer team.
5. Connect and trust the iPhone, select it as the run destination, and run the `Gripi` scheme.
6. Add the HTTPS, local-network, or private-VPN URL of a running gateway in the app.
7. Complete Gripi's normal browser or workspace access approval inside the gateway view.

The app accepts user-configured HTTP URLs because local and private-network development gateways may use them. Gripi's production gateway still rejects unsafe remote plaintext access unless it was explicitly configured otherwise. Prefer HTTPS whenever the network is not already encrypted.

## Capabilities

The embedded gateway UI retains session browsing and search, prompting and streaming, steering and follow-ups, images, bash commands, model and thinking controls, extension dialogs, session trees and forks, exports, and gateway operations.

The native shell adds:

- Multiple saved gateways with direct one-tap switching and unread counts
- Persistent, isolated WebKit cookies and website data for each gateway
- Strict same-origin navigation and native-bridge checks
- Safari handoff for external links
- In-app sheets for same-gateway popup pages
- Native clipboard writes
- HTML export through the iOS share sheet and Files destinations
- Local notifications and safe gateway/session routing when a notification is tapped

Removing a gateway clears its WebKit website data from the device.

## Notification boundary

The native app can present notifications for events its WebKit views observe while iOS allows the app to run. iOS suspends or terminates background apps, so reliable completion notifications while the native app is closed require APNs.

APNs delivery is intentionally deferred until Gripi chooses between:

- a central relay that protects the app's APNs provider key, or
- direct self-hosted delivery requiring each builder to supply Apple provider credentials.

Until then, the installed Home Screen web app remains the supported closed-app notification option through the gateway's existing Web Push integration.

## Verification

On the Mac, run the simulator tests:

```sh
xcodebuild \
  -project ios/Gripi.xcodeproj \
  -scheme Gripi \
  -destination 'platform=iOS Simulator,name=iPhone 16 Pro' \
  CODE_SIGNING_ALLOWED=NO \
  test
```

If that simulator model is unavailable, select an installed iPhone simulator in Xcode or replace the destination name.

Before relying on a device build, verify:

1. First launch accepts a gateway and completes access approval.
2. Adding a second gateway and tapping the first gateway switches it immediately.
3. Both gateways retain independent login state after relaunch.
4. External links open outside Gripi; the session-only action opens an in-app sheet.
5. Message and code-block copy buttons update the iPhone clipboard.
6. Session HTML export opens the share sheet and saves successfully to Files.
7. Enabling notifications from the gateway UI shows Apple's permission prompt, and the notification test opens the correct gateway when tapped.
8. Prompt streaming, image attachment, extension dialogs, background-session unread counts, and reconnect behavior work on the physical phone.

The GitHub `iOS` workflow runs unit and UI tests on a macOS simulator. Linux contributors can still check Swift syntax without Apple frameworks when a Swift toolchain is installed:

```sh
swiftc -frontend -parse $(find ios/Gripi ios/GripiTests ios/GripiUITests -name '*.swift' -print)
```
