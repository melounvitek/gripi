import SwiftUI

struct GatewayScreen: View {
    @ObservedObject var session: GatewayWebViewSession

    var body: some View {
        ZStack {
            GatewayWebView(session: session)

            if let failureMessage = session.failureMessage {
                unavailableView(failureMessage)
            } else if session.isLoading {
                ProgressView("Opening \(session.gateway.name)…")
                    .padding(20)
                    .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 16))
            }
        }
        .sheet(item: $session.popupRequest) { request in
            PopupGatewayView(request: request)
        }
    }

    private func unavailableView(_ message: String) -> some View {
        ContentUnavailableView {
            Label("Server Not Reachable", systemImage: "network.slash")
        } description: {
            Text("\(session.gateway.url.absoluteString)\n\n\(message)")
        } actions: {
            Button("Retry") { session.retry() }
                .buttonStyle(.borderedProminent)
        }
        .padding()
        .background(.background)
    }
}

private struct PopupGatewayView: View {
    @Environment(\.dismiss) private var dismiss
    @StateObject private var session: GatewayWebViewSession

    init(request: PopupRequest) {
        _session = StateObject(wrappedValue: GatewayWebViewSession(gateway: request.gateway, initialURL: request.url))
    }

    var body: some View {
        NavigationStack {
            GatewayScreen(session: session)
                .navigationTitle(session.gateway.name)
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .confirmationAction) {
                        Button("Done") { dismiss() }
                    }
                }
        }
    }
}
