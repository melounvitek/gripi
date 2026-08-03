import Foundation

struct Gateway: Codable, Equatable, Identifiable {
    let id: UUID
    var name: String
    var url: URL

    init(id: UUID = UUID(), name: String, url: URL) {
        self.id = id
        self.name = name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? Self.defaultName(for: url) : name.trimmingCharacters(in: .whitespacesAndNewlines)
        self.url = url
    }

    static func from(id: UUID = UUID(), name: String, url input: String) throws -> Gateway {
        Gateway(id: id, name: name, url: try normalizedURL(input))
    }

    static func normalizedURL(_ input: String) throws -> URL {
        let value = input.trimmingCharacters(in: .whitespacesAndNewlines)
        guard var components = URLComponents(string: value),
              let scheme = components.scheme?.lowercased(),
              ["http", "https"].contains(scheme),
              components.host?.isEmpty == false,
              components.user == nil,
              components.password == nil else {
            throw GatewayValidationError.invalidURL
        }

        components.scheme = scheme
        components.fragment = nil
        if components.path.isEmpty { components.path = "/" }

        guard let url = components.url else { throw GatewayValidationError.invalidURL }
        return url
    }

    private static func defaultName(for url: URL) -> String {
        url.host(percentEncoded: false) ?? "Pi Server"
    }
}

enum GatewayValidationError: LocalizedError, Equatable {
    case invalidURL
    case onlyGateway
    case notFound

    var errorDescription: String? {
        switch self {
        case .invalidURL:
            "Enter an HTTP or HTTPS URL without embedded credentials."
        case .onlyGateway:
            "The only server cannot be removed."
        case .notFound:
            "Server not found."
        }
    }
}
