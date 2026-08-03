import Combine
import Foundation

@MainActor
final class GatewayStore: ObservableObject {
    @Published private(set) var configuration: GatewayConfiguration
    @Published private(set) var errorMessage: String?

    private let repository: GatewayConfigurationRepository

    init(repository: GatewayConfigurationRepository = UserDefaultsGatewayRepository()) {
        self.repository = repository
        configuration = repository.load()
    }

    var activeGateway: Gateway? { configuration.activeGateway }

    func add(name: String, url: String) -> Bool {
        change {
            $0.add(try Gateway.from(name: name, url: url))
        }
    }

    func save(id: UUID, name: String, url: String) -> Bool {
        change {
            $0.save(try Gateway.from(id: id, name: name, url: url))
        }
    }

    func activate(_ id: UUID) {
        change { try $0.activate(id) }
    }

    func remove(_ id: UUID) -> Bool {
        change { try $0.remove(id) }
    }

    func clearError() {
        errorMessage = nil
    }

    @discardableResult
    private func change(_ update: (inout GatewayConfiguration) throws -> Void) -> Bool {
        var updatedConfiguration = configuration

        do {
            try update(&updatedConfiguration)
            try repository.save(updatedConfiguration)
            configuration = updatedConfiguration
            errorMessage = nil
            return true
        } catch {
            errorMessage = error.localizedDescription
            return false
        }
    }
}
