const test = require("node:test");
const assert = require("node:assert/strict");
const { DEFAULT_GATEWAY_URL, gatewayUrl } = require("./gateway_url");

test("uses localhost gateway URL by default", () => {
  assert.equal(gatewayUrl({}, ["electron", "."]), DEFAULT_GATEWAY_URL);
});

test("uses PI_GATEWAY_DESKTOP_URL when present", () => {
  assert.equal(
    gatewayUrl({ PI_GATEWAY_DESKTOP_URL: "https://pi.example.test/gateway" }, []),
    "https://pi.example.test/gateway"
  );
});

test("uses --gateway-url before environment configuration", () => {
  assert.equal(
    gatewayUrl(
      { PI_GATEWAY_DESKTOP_URL: "http://env.example.test" },
      ["electron", ".", "--gateway-url=http://arg.example.test"]
    ),
    "http://arg.example.test/"
  );
});

test("uses configured URL when environment and argument are absent", () => {
  assert.equal(
    gatewayUrl({}, ["electron", "."], "http://configured.example.test:4567"),
    "http://configured.example.test:4567/"
  );
});

test("uses environment configuration before persisted configuration", () => {
  assert.equal(
    gatewayUrl({ PI_GATEWAY_DESKTOP_URL: "https://env.example.test" }, [], "http://configured.example.test:4567"),
    "https://env.example.test/"
  );
});

test("falls back to localhost for unsupported URL schemes", () => {
  assert.equal(
    gatewayUrl({ PI_GATEWAY_DESKTOP_URL: "file:///tmp/gateway.html" }, [], "http://configured.example.test:4567"),
    DEFAULT_GATEWAY_URL
  );
});

test("falls back to localhost when --gateway-url is invalid", () => {
  assert.equal(
    gatewayUrl({ PI_GATEWAY_DESKTOP_URL: "https://pi.example.test" }, ["electron", ".", "--gateway-url=not a url"]),
    DEFAULT_GATEWAY_URL
  );
});
