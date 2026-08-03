import SwiftUI

struct ContentView: View {
    @EnvironmentObject private var gatewayStore: GatewayStore
    @EnvironmentObject private var gatewaySessions: GatewaySessionStore

    @State private var editor: GatewayEditor?
    @State private var pendingRemoval: Gateway?

    var body: some View {
        Group {
            if gatewayStore.configuration.gateways.isEmpty {
                GatewayEditorView()
            } else {
                VStack(spacing: 0) {
                    GatewayToolbar(
                        addGateway: { editor = .add },
                        editGateway: { editor = .edit($0) },
                        removeGateway: { pendingRemoval = $0 }
                    )
                    gatewayViews
                }
            }
        }
        .preferredColorScheme(.dark)
        .sheet(item: $editor) { editor in
            switch editor {
            case .add:
                GatewayEditorView(dismissAfterSave: true)
            case let .edit(gateway):
                GatewayEditorView(gateway: gateway, dismissAfterSave: true)
            }
        }
        .alert("Remove Server?", isPresented: removalAlert, presenting: pendingRemoval) { gateway in
            Button("Remove", role: .destructive) {
                gatewayStore.remove(gateway.id)
                pendingRemoval = nil
            }
            Button("Cancel", role: .cancel) { pendingRemoval = nil }
        } message: { gateway in
            Text("Remove “\(gateway.name)” and its website data from this iPhone?")
        }
        .onAppear {
            gatewaySessions.synchronize(with: gatewayStore.configuration.gateways)
        }
        .onChange(of: gatewayStore.configuration.gateways) { _, gateways in
            gatewaySessions.synchronize(with: gateways)
        }
        .onReceive(NotificationCenter.default.publisher(for: .openGripiNotification)) { notification in
            openNotification(notification.userInfo)
        }
    }

    private var gatewayViews: some View {
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

    private var removalAlert: Binding<Bool> {
        Binding(
            get: { pendingRemoval != nil },
            set: { if !$0 { pendingRemoval = nil } }
        )
    }

    private func openNotification(_ userInfo: [AnyHashable: Any]?) {
        guard let idValue = userInfo?["gatewayID"] as? String,
              let gatewayID = UUID(uuidString: idValue),
              let urlValue = userInfo?["url"] as? String,
              let url = URL(string: urlValue),
              let gateway = gatewayStore.configuration.gateways.first(where: { $0.id == gatewayID }),
              OriginPolicy(gatewayURL: gateway.url)?.contains(url) == true else {
            return
        }

        gatewayStore.activate(gateway.id)
        gatewaySessions.session(for: gateway).load(url)
    }
}

private enum GatewayEditor: Identifiable {
    case add
    case edit(Gateway)

    var id: String {
        switch self {
        case .add:
            "add"
        case let .edit(gateway):
            "edit-\(gateway.id.uuidString)"
        }
    }
}
