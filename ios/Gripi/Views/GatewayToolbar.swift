import SwiftUI

struct GatewayToolbar: View {
    @EnvironmentObject private var gatewayStore: GatewayStore
    @EnvironmentObject private var gatewaySessions: GatewaySessionStore

    let addGateway: () -> Void
    let editGateway: (Gateway) -> Void
    let removeGateway: (Gateway) -> Void

    var body: some View {
        HStack(spacing: 8) {
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 6) {
                    ForEach(gatewayStore.configuration.gateways) { gateway in
                        gatewayButton(gateway)
                    }
                }
                .padding(.horizontal, 8)
            }

            Menu {
                Button("Add Server", systemImage: "plus") { addGateway() }
                if let gateway = gatewayStore.activeGateway {
                    Button("Edit \(gateway.name)", systemImage: "pencil") { editGateway(gateway) }
                    Button("Remove \(gateway.name)", systemImage: "trash", role: .destructive) { removeGateway(gateway) }
                        .disabled(gatewayStore.configuration.gateways.count == 1)
                }
            } label: {
                Image(systemName: "ellipsis.circle")
                    .font(.title3)
                    .frame(width: 36, height: 36)
            }
            .accessibilityLabel("Server options")
            .padding(.trailing, 6)
        }
        .frame(height: 44)
        .background(.ultraThinMaterial)
        .overlay(alignment: .bottom) { Divider() }
    }

    private func gatewayButton(_ gateway: Gateway) -> some View {
        let active = gateway.id == gatewayStore.activeGateway?.id
        let unreadCount = gatewaySessions.session(for: gateway).unreadCount

        return Button {
            gatewayStore.activate(gateway.id)
        } label: {
            HStack(spacing: 5) {
                Text(gateway.name)
                    .lineLimit(1)
                if unreadCount > 0 {
                    Text(unreadCount > 99 ? "99+" : String(unreadCount))
                        .font(.caption2.bold())
                        .padding(.horizontal, 5)
                        .padding(.vertical, 2)
                        .background(.orange, in: Capsule())
                        .foregroundStyle(.black)
                }
            }
            .padding(.horizontal, 11)
            .frame(height: 32)
            .background(active ? Color.secondary.opacity(0.24) : .clear, in: Capsule())
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("gateway-\(gateway.id.uuidString)")
        .accessibilityAddTraits(active ? .isSelected : [])
    }
}
