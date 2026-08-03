import Foundation

struct WebOrigin: Equatable {
    let scheme: String
    let host: String
    let port: Int

    init?(url: URL) {
        let originURL: URL
        if url.scheme?.lowercased() == "blob" {
            let value = String(url.absoluteString.dropFirst("blob:".count))
            guard let nestedURL = URL(string: value) else { return nil }
            originURL = nestedURL
        } else {
            originURL = url
        }

        guard let scheme = originURL.scheme?.lowercased(),
              ["http", "https"].contains(scheme),
              let host = originURL.host(percentEncoded: false)?.lowercased() else {
            return nil
        }

        self.scheme = scheme
        self.host = host
        port = originURL.port ?? (scheme == "https" ? 443 : 80)
    }
}

enum NavigationDecision: Equatable {
    case allow
    case openExternally
    case reject
}

struct OriginPolicy {
    let origin: WebOrigin

    init?(gatewayURL: URL) {
        guard let origin = WebOrigin(url: gatewayURL) else { return nil }
        self.origin = origin
    }

    func decision(for url: URL) -> NavigationDecision {
        if url.absoluteString == "about:blank" { return .allow }
        if WebOrigin(url: url) == origin { return .allow }
        if ["http", "https", "mailto", "tel"].contains(url.scheme?.lowercased() ?? "") { return .openExternally }
        return .reject
    }

    func contains(_ url: URL) -> Bool {
        WebOrigin(url: url) == origin
    }
}
