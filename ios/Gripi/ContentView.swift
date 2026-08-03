import SwiftUI

struct ContentView: View {
    @EnvironmentObject private var gatewayStore: GatewayStore

    var body: some View {
        Group {
            if let gateway = gatewayStore.activeGateway {
                Text("Opening \(gateway.name)…")
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                GatewayEditorView()
            }
        }
        .preferredColorScheme(.dark)
    }
}
