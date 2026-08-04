import XCTest
@testable import Gripi

@MainActor
final class NativeIntegrationTests: XCTestCase {
    func testNotificationPayloadResolvesOnlyGatewayURLs() throws {
        let gatewayURL = try XCTUnwrap(URL(string: "https://gateway.example/"))
        let payload = try XCTUnwrap(NativeNotificationPayload(message: [
            "type": "gripi-notification",
            "title": " Session finished ",
            "body": "Done",
            "url": "/?session=one",
            "tag": "session-one"
        ], gatewayURL: gatewayURL))

        XCTAssertEqual(payload.title, "Session finished")
        XCTAssertEqual(payload.url.absoluteString, "https://gateway.example/?session=one")
        XCTAssertEqual(payload.tag, "session-one")
        XCTAssertNil(NativeNotificationPayload(message: [
            "type": "gripi-notification",
            "url": "https://attacker.example/"
        ], gatewayURL: gatewayURL))
    }

    func testNotificationPayloadRejectsUnknownMessageTypes() throws {
        let gatewayURL = try XCTUnwrap(URL(string: "https://gateway.example/"))

        XCTAssertNil(NativeNotificationPayload(message: [
            "type": "untrusted",
            "url": "/"
        ], gatewayURL: gatewayURL))
    }

    func testNativeBridgeSupportsGatewaysUsingTheElectronContract() {
        XCTAssertTrue(NativeBridge.source.contains("window.gripiElectron ||="))
        XCTAssertTrue(NativeBridge.source.contains("payload?.type === \"gripi-notification-test\""))
    }

    func testRenamingAGatewayPreservesItsWebViewSession() throws {
        let gateway = try Gateway.from(name: "Original", url: "https://gateway.example")
        let renamed = try Gateway.from(id: gateway.id, name: "Renamed", url: gateway.url.absoluteString)
        let sessions = GatewaySessionStore()

        let originalSession = sessions.session(for: gateway)
        let renamedSession = sessions.session(for: renamed)

        XCTAssertTrue(originalSession === renamedSession)
        XCTAssertEqual(renamedSession.gateway.name, "Renamed")
    }

    func testDownloadFilenameCannotEscapeTheTemporaryDirectory() {
        XCTAssertEqual(DownloadDestination.filename(from: "../../session.html"), "session.html")
        XCTAssertEqual(DownloadDestination.filename(from: "..\\..\\session.html"), "session.html")
        XCTAssertEqual(DownloadDestination.filename(from: ".."), "gripi-download")
        XCTAssertEqual(DownloadDestination.filename(from: "   "), "gripi-download")
    }
}
