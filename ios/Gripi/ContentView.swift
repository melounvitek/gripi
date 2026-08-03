import SwiftUI

struct ContentView: View {
    @EnvironmentObject private var gatewayStore: GatewayStore
    @EnvironmentObject private var gatewaySessions: GatewaySessionStore

    var body: some View {
        Group {
            if gatewayStore.configuration.gateways.isEmpty {
                GatewayEditorView()
            } else {
                ZStack {
                    ForEach(gatewayStore.configuration.gateways) { gateway in
                        let session = gatewaySessions.session(for: gateway)
                        GatewayScreen(session: session)
                            .id(session.id)
                            .opacity(gateway.id == gatewayStore.activeGateway?.id ? 1 : 0)
                            .allowsHitTesting(gateway.id == gatewayStore.activeGateway?.id)
                            .accessibilityHidden(gateway.id != gatewayStore.activeGateway?.id)
                    }
                }
            }
        }
        .preferredColorScheme(.dark)
        .onAppear {
            gatewaySessions.synchronize(with: gatewayStore.configuration.gateways)
        }
        .onChange(of: gatewayStore.configuration.gateways) { _, gateways in
            gatewaySessions.synchronize(with: gateways)
        }
    }
}
