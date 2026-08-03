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

    func session(for gateway: Gateway) -> GatewayWebViewSession {
        if let session = sessions[gateway.id], session.gateway == gateway { return session }

        let session = GatewayWebViewSession(gateway: gateway)
        sessions[gateway.id] = session
        return session
    }

    func synchronize(with gateways: [Gateway]) {
        let gatewayIDs = Set(gateways.map(\.id))
        let removedSessions = sessions.filter { !gatewayIDs.contains($0.key) }.map(\.value)
        sessions = sessions.filter { gatewayIDs.contains($0.key) }

        for session in removedSessions {
            Task { await session.clearWebsiteData() }
        }
    }
}
