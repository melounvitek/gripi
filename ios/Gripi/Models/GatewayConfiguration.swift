import Foundation

struct GatewayConfiguration: Codable, Equatable {
    private(set) var gateways: [Gateway]
    private(set) var activeGatewayID: UUID?

    init(gateways: [Gateway] = [], activeGatewayID: UUID? = nil) {
        self.gateways = gateways
        self.activeGatewayID = gateways.contains(where: { $0.id == activeGatewayID }) ? activeGatewayID : gateways.first?.id
    }

    var activeGateway: Gateway? {
        gateways.first(where: { $0.id == activeGatewayID }) ?? gateways.first
    }

    mutating func add(_ gateway: Gateway) {
        gateways.append(gateway)
        activeGatewayID = gateway.id
    }

    mutating func save(_ gateway: Gateway) {
        if let index = gateways.firstIndex(where: { $0.id == gateway.id }) {
            gateways[index] = gateway
        } else {
            gateways.append(gateway)
        }
        activeGatewayID = gateway.id
    }

    mutating func activate(_ id: UUID) throws {
        guard gateways.contains(where: { $0.id == id }) else { throw GatewayValidationError.notFound }
        activeGatewayID = id
    }

    mutating func remove(_ id: UUID) throws {
        guard gateways.count > 1 else { throw GatewayValidationError.onlyGateway }
        guard let index = gateways.firstIndex(where: { $0.id == id }) else { throw GatewayValidationError.notFound }

        gateways.remove(at: index)
        if activeGatewayID == id { activeGatewayID = gateways.first?.id }
    }
}
