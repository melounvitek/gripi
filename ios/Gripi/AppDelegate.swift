import Combine
import UIKit
import UserNotifications

struct NotificationRoute: Equatable {
    let gatewayID: String
    let url: String
}

@MainActor
final class NotificationRouter: ObservableObject {
    static let shared = NotificationRouter()

    @Published private(set) var route: NotificationRoute?

    func receive(_ userInfo: [AnyHashable: Any]) {
        guard let gatewayID = userInfo["gatewayID"] as? String,
              let url = userInfo["url"] as? String else {
            return
        }
        route = NotificationRoute(gatewayID: gatewayID, url: url)
    }

    func consume() {
        route = nil
    }
}

@MainActor
final class AppDelegate: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate {
    func application(
        _ application: UIApplication,
        didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil
    ) -> Bool {
        UNUserNotificationCenter.current().delegate = self
        return true
    }

    func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification,
        withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        completionHandler([.banner, .sound])
    }

    func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse,
        withCompletionHandler completionHandler: @escaping () -> Void
    ) {
        NotificationRouter.shared.receive(response.notification.request.content.userInfo)
        completionHandler()
    }
}
