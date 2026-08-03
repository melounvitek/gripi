import SwiftUI

@main
struct GripiApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @StateObject private var gatewayStore = GatewayStore()
    @StateObject private var gatewaySessions = GatewaySessionStore()
    @StateObject private var notificationRouter = NotificationRouter.shared

    var body: some Scene {
        WindowGroup {
            ContentView()
                .environmentObject(gatewayStore)
                .environmentObject(gatewaySessions)
                .environmentObject(notificationRouter)
        }
    }
}
