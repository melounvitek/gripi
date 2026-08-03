import Combine
import Foundation
import UIKit
import WebKit

struct PopupRequest: Identifiable {
    let id = UUID()
    let gateway: Gateway
    let url: URL
}

@MainActor
final class GatewayWebViewSession: NSObject, ObservableObject, Identifiable {
    let id = UUID()
    let gateway: Gateway
    let webView: WKWebView

    @Published private(set) var isLoading = true
    @Published private(set) var failureMessage: String?
    @Published var popupRequest: PopupRequest?

    private let coordinator: GatewayWebCoordinator

    init(gateway: Gateway, initialURL: URL? = nil) {
        self.gateway = gateway

        let configuration = WKWebViewConfiguration()
        configuration.websiteDataStore = WKWebsiteDataStore(forIdentifier: gateway.id)
        configuration.defaultWebpagePreferences.allowsContentJavaScript = true
        webView = WKWebView(frame: .zero, configuration: configuration)
        coordinator = GatewayWebCoordinator(gateway: gateway)

        super.init()

        coordinator.session = self
        webView.navigationDelegate = coordinator
        webView.uiDelegate = coordinator
        webView.allowsBackForwardNavigationGestures = true
        webView.scrollView.keyboardDismissMode = .interactive
        load(initialURL ?? gateway.url)
    }

    func load(_ url: URL) {
        guard OriginPolicy(gatewayURL: gateway.url)?.contains(url) == true else { return }
        failureMessage = nil
        isLoading = true
        webView.load(URLRequest(url: url))
    }

    func retry() {
        failureMessage = nil
        isLoading = true
        if webView.url == nil {
            load(gateway.url)
        } else {
            webView.reload()
        }
    }

    func clearWebsiteData() async {
        await withCheckedContinuation { continuation in
            webView.configuration.websiteDataStore.removeData(
                ofTypes: WKWebsiteDataStore.allWebsiteDataTypes(),
                modifiedSince: .distantPast
            ) {
                continuation.resume()
            }
        }
    }

    fileprivate func startedLoading() {
        failureMessage = nil
        isLoading = true
    }

    fileprivate func finishedLoading() {
        failureMessage = nil
        isLoading = false
    }

    fileprivate func failedLoading(_ error: Error) {
        guard (error as NSError).code != NSURLErrorCancelled else { return }
        isLoading = false
        failureMessage = error.localizedDescription
    }
}

@MainActor
final class GatewayWebCoordinator: NSObject, WKNavigationDelegate, WKUIDelegate {
    weak var session: GatewayWebViewSession?

    private let gateway: Gateway
    private let originPolicy: OriginPolicy

    init(gateway: Gateway) {
        self.gateway = gateway
        originPolicy = OriginPolicy(gatewayURL: gateway.url)!
    }

    func webView(_ webView: WKWebView, didStartProvisionalNavigation navigation: WKNavigation?) {
        session?.startedLoading()
    }

    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation?) {
        session?.finishedLoading()
    }

    func webView(_ webView: WKWebView, didFail navigation: WKNavigation?, withError error: Error) {
        session?.failedLoading(error)
    }

    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation?, withError error: Error) {
        session?.failedLoading(error)
    }

    func webView(
        _ webView: WKWebView,
        decidePolicyFor navigationAction: WKNavigationAction,
        decisionHandler: @escaping (WKNavigationActionPolicy) -> Void
    ) {
        guard let url = navigationAction.request.url else {
            decisionHandler(.cancel)
            return
        }

        switch originPolicy.decision(for: url) {
        case .allow:
            decisionHandler(.allow)
        case .openExternally:
            decisionHandler(.cancel)
            UIApplication.shared.open(url)
        case .reject:
            decisionHandler(.cancel)
        }
    }

    func webView(
        _ webView: WKWebView,
        createWebViewWith configuration: WKWebViewConfiguration,
        for navigationAction: WKNavigationAction,
        windowFeatures: WKWindowFeatures
    ) -> WKWebView? {
        guard navigationAction.targetFrame == nil, let url = navigationAction.request.url else { return nil }

        switch originPolicy.decision(for: url) {
        case .allow:
            session?.popupRequest = PopupRequest(gateway: gateway, url: url)
        case .openExternally:
            UIApplication.shared.open(url)
        case .reject:
            break
        }
        return nil
    }
}
