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
    private(set) var gateway: Gateway
    let webView: WKWebView

    @Published private(set) var isLoading = true
    @Published private(set) var failureMessage: String?
    @Published private(set) var unreadCount = 0
    @Published var popupRequest: PopupRequest?
    @Published var shareRequest: ShareRequest?

    private let coordinator: GatewayWebCoordinator
    private let nativeBridge: NativeBridge
    private var unreadTimer: AnyCancellable?
    private var isActive = false

    init(gateway: Gateway, initialURL: URL? = nil) {
        self.gateway = gateway

        let contentController = WKUserContentController()
        nativeBridge = NativeBridge(gateway: gateway)
        contentController.addScriptMessageHandler(nativeBridge, contentWorld: .page, name: NativeBridge.handlerName)
        contentController.addUserScript(WKUserScript(
            source: NativeBridge.source,
            injectionTime: .atDocumentStart,
            forMainFrameOnly: true
        ))

        let configuration = WKWebViewConfiguration()
        configuration.websiteDataStore = WKWebsiteDataStore(forIdentifier: gateway.id)
        configuration.defaultWebpagePreferences.allowsContentJavaScript = true
        configuration.userContentController = contentController
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

    func update(_ gateway: Gateway) {
        guard self.gateway.id == gateway.id, self.gateway.url == gateway.url else { return }
        self.gateway = gateway
    }

    func setActive(_ active: Bool) {
        isActive = active
        updateNativeVisibility()
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
        refreshUnreadCount()
        updateNativeVisibility()
        if unreadTimer == nil {
            unreadTimer = Timer.publish(every: 5, on: .main, in: .common)
                .autoconnect()
                .sink { [weak self] _ in self?.refreshUnreadCount() }
        }
    }

    private func updateNativeVisibility() {
        webView.evaluateJavaScript("window.gripiNativeViewActive = \(isActive ? "true" : "false")")
    }

    private func refreshUnreadCount() {
        webView.evaluateJavaScript("Number(document.querySelector('.session-sidebar[data-unread-session-count]')?.dataset.unreadSessionCount || 0)") { [weak self] result, _ in
            guard let count = result as? Double else { return }
            self?.unreadCount = max(0, Int(count))
        }
    }

    fileprivate func failedLoading(_ error: Error) {
        guard (error as NSError).code != NSURLErrorCancelled else { return }
        isLoading = false
        failureMessage = error.localizedDescription
    }

    fileprivate func startedDownload() {
        isLoading = false
        failureMessage = nil
    }

    fileprivate func failedDownload() {
        isLoading = false
    }
}

@MainActor
final class GatewayWebCoordinator: NSObject, WKNavigationDelegate, WKUIDelegate, WKDownloadDelegate {
    weak var session: GatewayWebViewSession?

    private let gateway: Gateway
    private let originPolicy: OriginPolicy
    private var downloadDestinations: [ObjectIdentifier: URL] = [:]

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

    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
        session?.retry()
    }

    func webView(
        _ webView: WKWebView,
        decidePolicyFor navigationAction: WKNavigationAction,
        decisionHandler: @escaping @MainActor @Sendable (WKNavigationActionPolicy) -> Void
    ) {
        guard let url = navigationAction.request.url else {
            decisionHandler(.cancel)
            return
        }

        switch originPolicy.decision(for: url) {
        case .allow:
            decisionHandler(navigationAction.shouldPerformDownload ? .download : .allow)
        case .openExternally:
            decisionHandler(.cancel)
            UIApplication.shared.open(url)
        case .reject:
            decisionHandler(.cancel)
        }
    }

    func webView(
        _ webView: WKWebView,
        decidePolicyFor navigationResponse: WKNavigationResponse,
        decisionHandler: @escaping @MainActor @Sendable (WKNavigationResponsePolicy) -> Void
    ) {
        guard let url = navigationResponse.response.url, originPolicy.contains(url) else {
            decisionHandler(.cancel)
            return
        }

        decisionHandler(navigationResponse.canShowMIMEType ? .allow : .download)
    }

    func webView(_ webView: WKWebView, navigationAction: WKNavigationAction, didBecome download: WKDownload) {
        session?.startedDownload()
        download.delegate = self
    }

    func webView(_ webView: WKWebView, navigationResponse: WKNavigationResponse, didBecome download: WKDownload) {
        session?.startedDownload()
        download.delegate = self
    }

    func download(
        _ download: WKDownload,
        decideDestinationUsing response: URLResponse,
        suggestedFilename: String,
        completionHandler: @escaping @MainActor @Sendable (URL?) -> Void
    ) {
        let destination = DownloadDestination.temporaryURL(for: suggestedFilename)
        downloadDestinations[ObjectIdentifier(download)] = destination
        completionHandler(destination)
    }

    func download(
        _ download: WKDownload,
        willPerformHTTPRedirection response: HTTPURLResponse,
        newRequest request: URLRequest,
        decisionHandler: @escaping @MainActor @Sendable (WKDownload.RedirectPolicy) -> Void
    ) {
        guard let url = request.url, originPolicy.contains(url) else {
            decisionHandler(.cancel)
            return
        }
        decisionHandler(.allow)
    }

    func downloadDidFinish(_ download: WKDownload) {
        guard let destination = downloadDestinations.removeValue(forKey: ObjectIdentifier(download)) else { return }
        session?.shareRequest = ShareRequest(fileURL: destination)
    }

    func download(_ download: WKDownload, didFailWithError error: Error, resumeData: Data?) {
        if let destination = downloadDestinations.removeValue(forKey: ObjectIdentifier(download)) {
            try? FileManager.default.removeItem(at: destination.deletingLastPathComponent())
        }
        session?.failedDownload()
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
            session?.popupRequest = PopupRequest(gateway: session?.gateway ?? gateway, url: url)
        case .openExternally:
            UIApplication.shared.open(url)
        case .reject:
            break
        }
        return nil
    }
}
