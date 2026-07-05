const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { readGatewayUrl, writeGatewayUrl } = require("./gateway_config");

function withTempConfig(callback) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "pi-gateway-desktop-"));
  const file = path.join(dir, "config.json");
  try {
    callback(file);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
}

test("returns null when no config file exists", () => {
  withTempConfig((file) => {
    assert.equal(readGatewayUrl(file), null);
  });
});

test("reads a configured gateway URL", () => {
  withTempConfig((file) => {
    fs.writeFileSync(file, JSON.stringify({ gatewayUrl: "https://pi.example.test" }));

    assert.equal(readGatewayUrl(file), "https://pi.example.test/");
  });
});

test("ignores invalid configured gateway URLs", () => {
  withTempConfig((file) => {
    fs.writeFileSync(file, JSON.stringify({ gatewayUrl: "file:///tmp/gateway.html" }));

    assert.equal(readGatewayUrl(file), null);
  });
});

test("writes a normalized gateway URL", () => {
  withTempConfig((file) => {
    assert.equal(writeGatewayUrl(file, "http://100.64.0.10:4567"), "http://100.64.0.10:4567/");
    assert.deepEqual(JSON.parse(fs.readFileSync(file, "utf8")), { gatewayUrl: "http://100.64.0.10:4567/" });
  });
});

test("refuses to write invalid gateway URLs", () => {
  withTempConfig((file) => {
    assert.throws(() => writeGatewayUrl(file, "not a url"), /Enter an http or https URL/);
    assert.equal(fs.existsSync(file), false);
  });
});
