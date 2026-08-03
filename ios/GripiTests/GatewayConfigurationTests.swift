import XCTest
@testable import Gripi

final class GatewayConfigurationTests: XCTestCase {
    func testNormalizesSupportedGatewayURLs() throws {
        XCTAssertEqual(try Gateway.normalizedURL("  HTTPS://gateway.example  ").absoluteString, "https://gateway.example/")
        XCTAssertEqual(try Gateway.normalizedURL("http://192.0.2.1:4567/path?q=1#ignored").absoluteString, "http://192.0.2.1:4567/path?q=1")
    }

    func testRejectsUnsupportedOrCredentialBearingURLs() {
        for value in ["gateway.example", "file:///tmp/gripi", "javascript:alert(1)", "https://user:secret@gateway.example/"] {
            XCTAssertThrowsError(try Gateway.normalizedURL(value), value)
        }
    }

    func testAddingAndSavingActivateTheChangedGateway() throws {
        let first = try Gateway.from(name: "First", url: "https://first.example")
        let second = try Gateway.from(name: "Second", url: "https://second.example")
        var configuration = GatewayConfiguration()

        configuration.add(first)
        configuration.add(second)

        XCTAssertEqual(configuration.activeGateway, second)

        let renamed = Gateway(id: first.id, name: "Renamed", url: first.url)
        configuration.save(renamed)

        XCTAssertEqual(configuration.activeGateway, renamed)
        XCTAssertEqual(configuration.gateways.count, 2)
    }

    func testRemovingTheActiveGatewaySelectsTheFirstRemainingGateway() throws {
        let first = try Gateway.from(name: "First", url: "https://first.example")
        let second = try Gateway.from(name: "Second", url: "https://second.example")
        var configuration = GatewayConfiguration(gateways: [first, second], activeGatewayID: second.id)

        try configuration.remove(second.id)

        XCTAssertEqual(configuration.activeGateway, first)
    }

    func testOnlyGatewayCannotBeRemoved() throws {
        let gateway = try Gateway.from(name: "Only", url: "https://gateway.example")
        var configuration = GatewayConfiguration(gateways: [gateway])

        XCTAssertThrowsError(try configuration.remove(gateway.id)) { error in
            XCTAssertEqual(error as? GatewayValidationError, .onlyGateway)
        }
    }
}
