import SwiftUI

@main
struct GripiApp: App {
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
