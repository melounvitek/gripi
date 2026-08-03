import SwiftUI

@main
struct GripiApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @StateObject private var gatewayStore = GatewayStore()
    @StateObject private var gatewaySessions = GatewaySessionStore()

    var body: some Scene {
        WindowGroup {
            ContentView()
                .environmentObject(gatewayStore)
                .environmentObject(gatewaySessions)
        }
    }
}
