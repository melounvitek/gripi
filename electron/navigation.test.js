const test = require("node:test");
const assert = require("node:assert/strict");
const { allowsGatewayNavigation } = require("./navigation");

const GATEWAY_ORIGIN = "https://gripi.example.test";

test("allows navigation within the gateway origin", () => {
  assert.equal(allowsGatewayNavigation("https://gripi.example.test/cdn-cgi/access/authorized?nonce=abc", GATEWAY_ORIGIN), true);
});

test("allows the Cloudflare Access login", () => {
  assert.equal(
    allowsGatewayNavigation("https://team.cloudflareaccess.com/cdn-cgi/access/login/gripi.example.test?redirect_url=%2F", GATEWAY_ORIGIN),
    true
  );
});

test("rejects Cloudflare Access over plain HTTP", () => {
  assert.equal(allowsGatewayNavigation("http://team.cloudflareaccess.com/cdn-cgi/access/login/gripi.example.test", GATEWAY_ORIGIN), false);
});

test("rejects hosts that only look like Cloudflare Access", () => {
  assert.equal(allowsGatewayNavigation("https://team.cloudflareaccess.com.evil.test/", GATEWAY_ORIGIN), false);
  assert.equal(allowsGatewayNavigation("https://evilcloudflareaccess.com/", GATEWAY_ORIGIN), false);
});

test("rejects other origins", () => {
  assert.equal(allowsGatewayNavigation("https://example.test/", GATEWAY_ORIGIN), false);
  assert.equal(allowsGatewayNavigation("not a url", GATEWAY_ORIGIN), false);
});
