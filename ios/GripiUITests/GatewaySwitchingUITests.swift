import XCTest

final class GatewaySwitchingUITests: XCTestCase {
    func testGatewaySwitchActivatesOnTheFirstTap() {
        let app = XCUIApplication()
        app.launchEnvironment["GRIPI_GATEWAY_CONFIGURATION_KEY"] = "ui-test-\(UUID().uuidString)"
        app.launch()

        addInitialGateway(named: "First", url: "https://first.example", in: app)

        app.buttons["Server options"].tap()
        app.buttons["Add Server"].tap()
        fillGatewayForm(named: "Second", url: "https://second.example", in: app)
        app.navigationBars["Connect to Gripi"].buttons["Save"].tap()

        let firstGateway = app.buttons["First"]
        XCTAssertTrue(firstGateway.waitForExistence(timeout: 2))

        firstGateway.tap()

        XCTAssertTrue(firstGateway.isSelected)
    }

    private func addInitialGateway(named name: String, url: String, in app: XCUIApplication) {
        fillGatewayForm(named: name, url: url, in: app)
        app.navigationBars["Connect to Gripi"].buttons["Save"].tap()
        XCTAssertTrue(app.buttons["Server options"].waitForExistence(timeout: 2))
    }

    private func fillGatewayForm(named name: String, url: String, in app: XCUIApplication) {
        let nameField = app.textFields["Server name"]
        XCTAssertTrue(nameField.waitForExistence(timeout: 2))
        nameField.tap()
        nameField.typeText(name)

        let urlField = app.textFields["https://gripi.example.com/"]
        urlField.tap()
        urlField.press(forDuration: 1)
        app.menuItems["Select All"].tap()
        urlField.typeText(url)
    }
}
