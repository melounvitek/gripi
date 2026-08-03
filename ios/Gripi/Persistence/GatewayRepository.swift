import Foundation

protocol GatewayConfigurationRepository {
    func load() -> GatewayConfiguration
    func save(_ configuration: GatewayConfiguration) throws
}

struct UserDefaultsGatewayRepository: GatewayConfigurationRepository {
    private let defaults: UserDefaults
    private let key: String

    init(defaults: UserDefaults = .standard, key: String = "gatewayConfiguration") {
        self.defaults = defaults
        self.key = key
    }

    func load() -> GatewayConfiguration {
        guard let data = defaults.data(forKey: key),
              let configuration = try? JSONDecoder().decode(GatewayConfiguration.self, from: data) else {
            return GatewayConfiguration()
        }

        return configuration
    }

    func save(_ configuration: GatewayConfiguration) throws {
        defaults.set(try JSONEncoder().encode(configuration), forKey: key)
    }
}
