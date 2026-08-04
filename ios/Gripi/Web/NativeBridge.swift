import Foundation
import UIKit
import UserNotifications
import WebKit

struct NativeNotificationPayload: Equatable {
    let title: String
    let body: String
    let url: URL
    let tag: String?

    init?(message: [String: Any], gatewayURL: URL) {
        guard let type = message["type"] as? String,
              ["gripi-notification", "gripi-notification-test"].contains(type),
              let originPolicy = OriginPolicy(gatewayURL: gatewayURL) else {
            return nil
        }

        let title = Self.trimmed(message["title"], fallback: "Gripi")
        let body = Self.trimmed(message["body"], fallback: "Notification from Gripi.")
        let candidate = (message["url"] as? String) ?? "/"
        guard let url = URL(string: candidate, relativeTo: gatewayURL)?.absoluteURL,
              originPolicy.contains(url) else {
            return nil
        }

        self.title = title
        self.body = body
        self.url = url
        tag = (message["tag"] as? String).flatMap { value in
            let trimmed = value.trimmingCharacters(in: .whitespacesAndNewlines)
            return trimmed.isEmpty ? nil : String(trimmed.prefix(500))
        }
    }

    private static func trimmed(_ value: Any?, fallback: String) -> String {
        guard let string = value as? String else { return fallback }
        let trimmed = string.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty ? fallback : String(trimmed.prefix(500))
    }
}

enum NativeNotificationPermission: String {
    case notDetermined
    case denied
    case granted
}

@MainActor
final class NativeNotificationService {
    static let shared = NativeNotificationService()

    private let center = UNUserNotificationCenter.current()

    func permission() async -> NativeNotificationPermission {
        switch await center.notificationSettings().authorizationStatus {
        case .notDetermined:
            .notDetermined
        case .denied:
            .denied
        case .authorized, .provisional, .ephemeral:
            .granted
        @unknown default:
            .denied
        }
    }

    func requestPermission() async -> NativeNotificationPermission {
        let currentPermission = await permission()
        if currentPermission == .denied {
            if let settingsURL = URL(string: UIApplication.openSettingsURLString) {
                await UIApplication.shared.open(settingsURL)
            }
            return .denied
        }
        if currentPermission == .granted { return .granted }

        do {
            _ = try await center.requestAuthorization(options: [.alert, .badge, .sound])
            return await permission()
        } catch {
            return .denied
        }
    }

    func show(_ payload: NativeNotificationPayload, gatewayID: UUID) async -> Bool {
        guard await permission() == .granted else { return false }

        let content = UNMutableNotificationContent()
        content.title = payload.title
        content.body = payload.body
        content.sound = .default
        content.userInfo = [
            "gatewayID": gatewayID.uuidString,
            "url": payload.url.absoluteString
        ]

        let request = UNNotificationRequest(
            identifier: "\(gatewayID.uuidString):\(payload.tag ?? UUID().uuidString)",
            content: content,
            trigger: nil
        )

        do {
            try await center.add(request)
            return true
        } catch {
            return false
        }
    }
}

@MainActor
final class NativeBridge: NSObject, WKScriptMessageHandlerWithReply {
    static let handlerName = "gripiNative"

    private let gateway: Gateway
    private let originPolicy: OriginPolicy

    init(gateway: Gateway) {
        self.gateway = gateway
        originPolicy = OriginPolicy(gatewayURL: gateway.url)!
    }

    func userContentController(
        _ userContentController: WKUserContentController,
        didReceive message: WKScriptMessage,
        replyHandler: @escaping @MainActor @Sendable (Any?, String?) -> Void
    ) {
        guard message.frameInfo.isMainFrame,
              let frameURL = message.frameInfo.request.url,
              originPolicy.contains(frameURL),
              let body = message.body as? [String: Any],
              let action = body["action"] as? String else {
            replyHandler(["ok": false], nil)
            return
        }

        switch action {
        case "copyText":
            guard let payload = body["payload"] as? [String: Any], let text = payload["text"] as? String else {
                replyHandler(["ok": false], nil)
                return
            }
            UIPasteboard.general.string = text
            replyHandler(["ok": true], nil)
        case "notificationPermission":
            Task {
                let permission = await NativeNotificationService.shared.permission()
                replyHandler(Self.permissionResponse(permission), nil)
            }
        case "requestNotificationPermission":
            Task {
                let permission = await NativeNotificationService.shared.requestPermission()
                replyHandler(Self.permissionResponse(permission), nil)
            }
        case "showNotification":
            guard let payload = body["payload"] as? [String: Any],
                  let notification = NativeNotificationPayload(message: payload, gatewayURL: gateway.url) else {
                replyHandler(["ok": false], nil)
                return
            }
            Task {
                let shown = await NativeNotificationService.shared.show(notification, gatewayID: gateway.id)
                let permission = await NativeNotificationService.shared.permission()
                replyHandler(["ok": shown, "status": permission.rawValue], nil)
            }
        default:
            replyHandler(["ok": false], nil)
        }
    }

    private static func permissionResponse(_ permission: NativeNotificationPermission) -> [String: Any] {
        ["ok": permission == .granted, "status": permission.rawValue]
    }

    static let source = """
    (() => {
      const handler = window.webkit?.messageHandlers?.gripiNative;
      if (!handler) return;
      const call = (action, payload = {}) => Promise.resolve(handler.postMessage({ action, payload }));
      window.gripiNativeViewActive = false;
      const nativeBridge = Object.freeze({
        notificationsRequirePermission: true,
        copyText: (text) => call("copyText", { text }),
        notificationPermission: () => call("notificationPermission"),
        requestNotificationPermission: () => call("requestNotificationPermission"),
        showNotification: (payload) => call("showNotification", payload)
      });
      window.gripiNative = nativeBridge;
      window.gripiElectron ||= Object.freeze({
        copyText: nativeBridge.copyText,
        showNotification: async (payload) => {
          let permission = await nativeBridge.notificationPermission();
          if (!permission?.ok && payload?.type === "gripi-notification-test") {
            permission = await nativeBridge.requestNotificationPermission();
          }
          if (!permission?.ok) return { ok: false, status: permission?.status };
          return nativeBridge.showNotification(payload);
        }
      });
    })();
    """
}
