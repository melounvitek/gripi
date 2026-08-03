import Foundation

protocol GatewayConfigurationRepository {
    func load() -> GatewayConfiguration
    func save(_ configuration: GatewayConfiguration) throws
}

struct UserDefaultsGatewayRepository: GatewayConfigurationRepository {
    private let defaults: UserDefaults
    private let key: String

    init(defaults: UserDefaults = .standard, key: String? = nil) {
        self.defaults = defaults
        self.key = key ?? ProcessInfo.processInfo.environment["GRIPI_GATEWAY_CONFIGURATION_KEY"] ?? "gatewayConfiguration"
    }

    func load() -> GatewayConfiguration {
        guard let data = defaults.data(forKey: key),
              let decoded = try? JSONDecoder().decode(GatewayConfiguration.self, from: data) else {
            return GatewayConfiguration()
        }

        let gateways = decoded.gateways.compactMap { gateway in
            try? Gateway.from(id: gateway.id, name: gateway.name, url: gateway.url.absoluteString)
        }
        return GatewayConfiguration(gateways: gateways, activeGatewayID: decoded.activeGatewayID)
    }

    func save(_ configuration: GatewayConfiguration) throws {
        defaults.set(try JSONEncoder().encode(configuration), forKey: key)
    }
}
