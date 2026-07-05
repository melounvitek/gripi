const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const {
  DEFAULT_GATEWAY_URL,
  addGateway,
  readGatewayConfig,
  readOrCreateGatewayConfig,
  removeGateway,
  saveGateway,
  writeGatewayConfig
} = require("./gateway_config");

function withTempConfig(callback) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "pi-gateway-desktop-"));
  const file = path.join(dir, "config.json");
  let nextId = 1;
  const idGenerator = () => `id-${nextId++}`;

  try {
    callback(file, idGenerator);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
}

test("creates a default localhost gateway when no config exists", () => {
  withTempConfig((file, idGenerator) => {
    assert.deepEqual(readGatewayConfig(file, idGenerator), {
      gateways: [{ id: "id-1", name: "Local", url: DEFAULT_GATEWAY_URL }],
      activeGatewayId: "id-1"
    });
  });
});

test("migrates the previous single gatewayUrl config", () => {
  withTempConfig((file, idGenerator) => {
    fs.writeFileSync(file, JSON.stringify({ gatewayUrl: "https://pi.example.test" }));

    assert.deepEqual(readGatewayConfig(file, idGenerator), {
      gateways: [{ id: "id-1", name: "Pi Server", url: "https://pi.example.test/" }],
      activeGatewayId: "id-1"
    });
  });
});

test("persists generated ids when creating a default config", () => {
  withTempConfig((file, idGenerator) => {
    const config = readOrCreateGatewayConfig(file, idGenerator);

    assert.equal(config.activeGatewayId, "id-1");
    assert.deepEqual(readGatewayConfig(file, idGenerator), config);
  });
});

test("persists generated ids when migrating old gatewayUrl config", () => {
  withTempConfig((file, idGenerator) => {
    fs.writeFileSync(file, JSON.stringify({ gatewayUrl: "https://pi.example.test" }));
    const config = readOrCreateGatewayConfig(file, idGenerator);

    assert.equal(config.activeGatewayId, "id-1");
    assert.deepEqual(readGatewayConfig(file, idGenerator), config);
  });
});

test("reads valid gateway lists and drops invalid entries", () => {
  withTempConfig((file, idGenerator) => {
    fs.writeFileSync(file, JSON.stringify({
      gateways: [
        { id: "good", name: "Good", url: "http://100.64.0.10:4567" },
        { id: "bad", name: "Bad", url: "file:///tmp/gateway.html" }
      ],
      activeGatewayId: "bad"
    }));

    assert.deepEqual(readGatewayConfig(file, idGenerator), {
      gateways: [{ id: "good", name: "Good", url: "http://100.64.0.10:4567/" }],
      activeGatewayId: "good"
    });
  });
});

test("writes normalized gateway config", () => {
  withTempConfig((file) => {
    writeGatewayConfig(file, {
      gateways: [{ id: "local", name: "Local", url: "http://localhost:4567" }],
      activeGatewayId: "local"
    });

    assert.deepEqual(JSON.parse(fs.readFileSync(file, "utf8")), {
      gateways: [{ id: "local", name: "Local", url: "http://localhost:4567/" }],
      activeGatewayId: "local"
    });
  });
});

test("adds a named gateway and makes it active", () => {
  withTempConfig((file, idGenerator) => {
    const config = readGatewayConfig(file, idGenerator);
    const nextConfig = addGateway(config, { name: "Mini", url: "http://100.64.0.10:4567" }, idGenerator);

    assert.deepEqual(nextConfig.gateways[1], { id: "id-2", name: "Mini", url: "http://100.64.0.10:4567/" });
    assert.equal(nextConfig.activeGatewayId, "id-2");
  });
});

test("updates an existing gateway and makes it active", () => {
  withTempConfig((file, idGenerator) => {
    const config = readGatewayConfig(file, idGenerator);
    const nextConfig = saveGateway(config, { id: "id-1", name: "Renamed", url: "https://pi.example.test" });

    assert.deepEqual(nextConfig, {
      gateways: [{ id: "id-1", name: "Renamed", url: "https://pi.example.test/" }],
      activeGatewayId: "id-1"
    });
  });
});

test("removes a gateway and activates another one", () => {
  withTempConfig((file, idGenerator) => {
    const config = addGateway(readGatewayConfig(file, idGenerator), { name: "Mini", url: "http://100.64.0.10:4567" }, idGenerator);
    const nextConfig = removeGateway(config, "id-2");

    assert.deepEqual(nextConfig, {
      gateways: [{ id: "id-1", name: "Local", url: DEFAULT_GATEWAY_URL }],
      activeGatewayId: "id-1"
    });
  });
});

test("keeps the active gateway when removing a different gateway", () => {
  withTempConfig((file, idGenerator) => {
    const config = addGateway(readGatewayConfig(file, idGenerator), { name: "Mini", url: "http://100.64.0.10:4567" }, idGenerator);
    const nextConfig = removeGateway(config, "id-1");

    assert.deepEqual(nextConfig, {
      gateways: [{ id: "id-2", name: "Mini", url: "http://100.64.0.10:4567/" }],
      activeGatewayId: "id-2"
    });
  });
});

test("refuses to remove the last gateway", () => {
  withTempConfig((file, idGenerator) => {
    const config = readGatewayConfig(file, idGenerator);

    assert.throws(() => removeGateway(config, "id-1"), /Cannot remove the only server/);
  });
});

test("refuses to remove an unknown gateway", () => {
  withTempConfig((file, idGenerator) => {
    const config = addGateway(readGatewayConfig(file, idGenerator), { name: "Mini", url: "http://100.64.0.10:4567" }, idGenerator);

    assert.throws(() => removeGateway(config, "missing"), /Server not found/);
  });
});

test("refuses invalid gateway URLs", () => {
  withTempConfig((file, idGenerator) => {
    const config = readGatewayConfig(file, idGenerator);

    assert.throws(() => addGateway(config, { name: "Bad", url: "not a url" }, idGenerator), /Enter an http or https URL/);
    assert.throws(() => saveGateway(config, { id: "id-1", name: "Bad", url: "file:\/\/\/tmp" }), /Enter an http or https URL/);
  });
});
