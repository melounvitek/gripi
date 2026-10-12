const test = require("node:test");
const assert = require("node:assert/strict");
const { installGatewayPermissionHandlers } = require("./permissions");

const GATEWAY_ORIGIN = "https://gripi.example.test";
const PAGE_URL = `${GATEWAY_ORIGIN}/sessions/abc`;

test("allows the gateway notifications, clipboard writes and the microphone", () => {
  const permissions = gatewayPermissions();

  assert.equal(permissions.request("notifications", { requestingUrl: PAGE_URL }), true);
  assert.equal(permissions.request("clipboard-sanitized-write", { requestingUrl: PAGE_URL }), true);
  assert.equal(permissions.request("media", { mediaTypes: ["audio"], requestingUrl: PAGE_URL, securityOrigin: `${GATEWAY_ORIGIN}/` }), true);
  assert.equal(permissions.check("media", `${GATEWAY_ORIGIN}/`, { mediaType: "audio", requestingUrl: PAGE_URL, securityOrigin: `${GATEWAY_ORIGIN}/` }), true);
});

test("never allows the camera", () => {
  const permissions = gatewayPermissions();

  assert.equal(permissions.request("media", { mediaTypes: ["video"], requestingUrl: PAGE_URL }), false);
  assert.equal(permissions.request("media", { mediaTypes: ["audio", "video"], requestingUrl: PAGE_URL }), false);
  assert.equal(permissions.request("media", { mediaTypes: [], requestingUrl: PAGE_URL }), false);
  assert.equal(permissions.check("media", `${GATEWAY_ORIGIN}/`, { mediaType: "video", requestingUrl: PAGE_URL }), false);
  assert.equal(permissions.check("media", `${GATEWAY_ORIGIN}/`, { mediaType: "unknown", requestingUrl: PAGE_URL }), false);
});

test("allows nothing else, and nothing to other origins", () => {
  const permissions = gatewayPermissions();

  assert.equal(permissions.request("geolocation", { requestingUrl: PAGE_URL }), false);
  assert.equal(permissions.request("notifications", { requestingUrl: "https://example.test/page" }), false);
  assert.equal(permissions.request("media", { mediaTypes: ["audio"], requestingUrl: "https://example.test/page" }), false);
  assert.equal(permissions.check("media", "https://example.test/", { mediaType: "audio", requestingUrl: "https://example.test/page" }), false);
});

// Calls the handlers as Electron 43 does, which passes the requesting frame's URL rather than a bare origin.
function gatewayPermissions() {
  const handlers = {};
  installGatewayPermissionHandlers({
    setPermissionCheckHandler: (handler) => { handlers.check = handler; },
    setPermissionRequestHandler: (handler) => { handlers.request = handler; }
  }, GATEWAY_ORIGIN);

  return {
    check: (permission, requestingOrigin, details) => handlers.check(null, permission, requestingOrigin, { isMainFrame: true, ...details }),
    request(permission, details) {
      let allowed;
      handlers.request(null, permission, (result) => { allowed = result; }, { isMainFrame: true, ...details });
      return allowed;
    }
  };
}
