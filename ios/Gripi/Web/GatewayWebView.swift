import Combine
import SwiftUI
import WebKit

struct GatewayWebView: UIViewRepresentable {
    let session: GatewayWebViewSession

    func makeUIView(context: Context) -> WKWebView {
        session.webView
    }

    func updateUIView(_ webView: WKWebView, context: Context) {}
}

@MainActor
final class GatewaySessionStore: ObservableObject {
    private var sessions: [UUID: GatewayWebViewSession] = [:]
    private var subscriptions: [UUID: AnyCancellable] = [:]

    func session(for gateway: Gateway) -> GatewayWebViewSession {
        if let session = sessions[gateway.id], session.gateway.url == gateway.url {
            session.update(gateway)
            return session
        }

        let session = GatewayWebViewSession(gateway: gateway)
        sessions[gateway.id] = session
        subscriptions[gateway.id] = session.objectWillChange.sink { [weak self] _ in
            self?.objectWillChange.send()
        }
        return session
    }

    func synchronize(with gateways: [Gateway]) {
        let gatewayIDs = Set(gateways.map(\.id))
        let removedSessions = sessions.filter { !gatewayIDs.contains($0.key) }.map(\.value)
        sessions = sessions.filter { gatewayIDs.contains($0.key) }
        subscriptions = subscriptions.filter { gatewayIDs.contains($0.key) }

        for session in removedSessions {
            Task { await session.clearWebsiteData() }
        }
    }
}
