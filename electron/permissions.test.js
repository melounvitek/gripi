const test = require("node:test");
const assert = require("node:assert/strict");
const { allowsGatewayPermission } = require("./permissions");

const GATEWAY_ORIGIN = "https://gripi.example.test";

test("allows the gateway notifications, clipboard writes and the microphone", () => {
  assert.equal(allowsGatewayPermission("notifications", GATEWAY_ORIGIN, [], GATEWAY_ORIGIN), true);
  assert.equal(allowsGatewayPermission("clipboard-sanitized-write", GATEWAY_ORIGIN, [], GATEWAY_ORIGIN), true);
  assert.equal(allowsGatewayPermission("media", GATEWAY_ORIGIN, ["audio"], GATEWAY_ORIGIN), true);
});

test("never allows the camera", () => {
  assert.equal(allowsGatewayPermission("media", GATEWAY_ORIGIN, ["video"], GATEWAY_ORIGIN), false);
  assert.equal(allowsGatewayPermission("media", GATEWAY_ORIGIN, ["audio", "video"], GATEWAY_ORIGIN), false);
  assert.equal(allowsGatewayPermission("media", GATEWAY_ORIGIN, ["unknown"], GATEWAY_ORIGIN), false);
  assert.equal(allowsGatewayPermission("media", GATEWAY_ORIGIN, [], GATEWAY_ORIGIN), false);
});

test("allows nothing else, and nothing to other origins", () => {
  assert.equal(allowsGatewayPermission("geolocation", GATEWAY_ORIGIN, [], GATEWAY_ORIGIN), false);
  assert.equal(allowsGatewayPermission("notifications", "https://example.test", [], GATEWAY_ORIGIN), false);
  assert.equal(allowsGatewayPermission("media", "https://example.test", ["audio"], GATEWAY_ORIGIN), false);
});
