import XCTest
@testable import Gripi

final class OriginPolicyTests: XCTestCase {
    func testAllowsGatewayOriginAcrossPaths() throws {
        let policy = try XCTUnwrap(OriginPolicy(gatewayURL: XCTUnwrap(URL(string: "https://gateway.example/app"))))

        XCTAssertEqual(policy.decision(for: XCTUnwrap(URL(string: "https://gateway.example/session?id=1"))), .allow)
        XCTAssertEqual(policy.decision(for: XCTUnwrap(URL(string: "https://gateway.example:443/other"))), .allow)
    }

    func testDistinguishesSchemesHostsAndPorts() throws {
        let policy = try XCTUnwrap(OriginPolicy(gatewayURL: XCTUnwrap(URL(string: "https://gateway.example:8443/"))))

        XCTAssertEqual(policy.decision(for: XCTUnwrap(URL(string: "http://gateway.example:8443/"))), .openExternally)
        XCTAssertEqual(policy.decision(for: XCTUnwrap(URL(string: "https://other.example:8443/"))), .openExternally)
        XCTAssertEqual(policy.decision(for: XCTUnwrap(URL(string: "https://gateway.example/"))), .openExternally)
    }

    func testAllowsBlobURLsOwnedByTheGateway() throws {
        let policy = try XCTUnwrap(OriginPolicy(gatewayURL: XCTUnwrap(URL(string: "https://gateway.example/"))))

        XCTAssertEqual(policy.decision(for: XCTUnwrap(URL(string: "blob:https://gateway.example/export-id"))), .allow)
        XCTAssertEqual(policy.decision(for: XCTUnwrap(URL(string: "blob:https://attacker.example/export-id"))), .reject)
    }

    func testExternalAndUnsafeSchemesHaveDifferentDecisions() throws {
        let policy = try XCTUnwrap(OriginPolicy(gatewayURL: XCTUnwrap(URL(string: "https://gateway.example/"))))

        XCTAssertEqual(policy.decision(for: XCTUnwrap(URL(string: "mailto:hello@example.com"))), .openExternally)
        XCTAssertEqual(policy.decision(for: XCTUnwrap(URL(string: "javascript:alert(1)"))), .reject)
        XCTAssertEqual(policy.decision(for: XCTUnwrap(URL(string: "file:///tmp/private"))), .reject)
    }
}
