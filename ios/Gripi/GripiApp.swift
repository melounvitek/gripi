import SwiftUI

@main
struct GripiApp: App {
    @StateObject private var gatewayStore = GatewayStore()

    var body: some Scene {
        WindowGroup {
            ContentView()
                .environmentObject(gatewayStore)
        }
    }
}
