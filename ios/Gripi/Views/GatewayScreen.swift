import SwiftUI

struct GatewayScreen: View {
    @ObservedObject var session: GatewayWebViewSession
    let isActive: Bool

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
        .onAppear { updateNativeVisibility() }
        .onChange(of: isActive) { _, _ in updateNativeVisibility() }
        .onChange(of: session.popupRequest?.id) { _, _ in updateNativeVisibility() }
        .onChange(of: session.shareRequest?.id) { _, _ in updateNativeVisibility() }
        .sheet(item: $session.popupRequest) { request in
            PopupGatewayView(request: request)
        }
        .sheet(item: $session.shareRequest) { request in
            ShareSheet(fileURL: request.fileURL) {
                session.shareRequest = nil
            }
        }
    }

    private func updateNativeVisibility() {
        session.setActive(isActive && session.popupRequest == nil && session.shareRequest == nil)
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
    @Environment(\.scenePhase) private var scenePhase
    @StateObject private var session: GatewayWebViewSession

    init(request: PopupRequest) {
        _session = StateObject(wrappedValue: GatewayWebViewSession(gateway: request.gateway, initialURL: request.url))
    }

    var body: some View {
        NavigationStack {
            GatewayScreen(session: session, isActive: scenePhase == .active)
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
