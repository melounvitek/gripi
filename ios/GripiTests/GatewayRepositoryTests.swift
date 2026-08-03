import XCTest
@testable import Gripi

final class GatewayRepositoryTests: XCTestCase {
    private var defaults: UserDefaults!
    private var suiteName: String!

    override func setUp() {
        super.setUp()
        suiteName = "GatewayRepositoryTests-\(UUID().uuidString)"
        defaults = UserDefaults(suiteName: suiteName)
    }

    override func tearDown() {
        defaults.removePersistentDomain(forName: suiteName)
        defaults = nil
        suiteName = nil
        super.tearDown()
    }

    func testRoundTripsConfiguration() throws {
        let gateway = try Gateway.from(name: "Home", url: "https://gateway.example")
        let configuration = GatewayConfiguration(gateways: [gateway])
        let repository = UserDefaultsGatewayRepository(defaults: defaults)

        try repository.save(configuration)

        XCTAssertEqual(repository.load(), configuration)
    }

    func testMissingOrMalformedDataLoadsEmptyConfiguration() {
        let repository = UserDefaultsGatewayRepository(defaults: defaults)
        XCTAssertTrue(repository.load().gateways.isEmpty)

        defaults.set(Data("not json".utf8), forKey: "gatewayConfiguration")

        XCTAssertTrue(repository.load().gateways.isEmpty)
        XCTAssertNil(repository.load().activeGateway)
    }
}
