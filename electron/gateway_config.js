const crypto = require("node:crypto");
const fs = require("node:fs");
const path = require("node:path");

const DEFAULT_GATEWAY_URL = "http://localhost:4567/";

function readGatewayConfig(configPath, idGenerator = crypto.randomUUID) {
  const rawConfig = readRawConfig(configPath);
  return normalizeConfig(rawConfig, idGenerator);
}

function readOrCreateGatewayConfig(configPath, idGenerator = crypto.randomUUID) {
  return writeGatewayConfig(configPath, readGatewayConfig(configPath, idGenerator));
}

function writeGatewayConfig(configPath, config) {
  const normalizedConfig = normalizeConfig(config, crypto.randomUUID);
  fs.mkdirSync(path.dirname(configPath), { recursive: true });
  fs.writeFileSync(configPath, `${JSON.stringify(normalizedConfig, null, 2)}\n`);
  return normalizedConfig;
}

function addGateway(config, gateway, idGenerator = crypto.randomUUID) {
  const normalizedGateway = normalizeGateway({ ...gateway, id: idGenerator() });
  const gateways = [...config.gateways, normalizedGateway];
  return { gateways, activeGatewayId: normalizedGateway.id };
}

function saveGateway(config, gateway) {
  const normalizedGateway = normalizeGateway(gateway);
  const gateways = config.gateways.map((existingGateway) => (
    existingGateway.id === normalizedGateway.id ? normalizedGateway : existingGateway
  ));

  if (!gateways.some((existingGateway) => existingGateway.id === normalizedGateway.id)) {
    gateways.push(normalizedGateway);
  }

  return { gateways, activeGatewayId: normalizedGateway.id };
}

function removeGateway(config, gatewayId) {
  if (config.gateways.length <= 1) throw new Error("Cannot remove the only server.");

  const gateways = config.gateways.filter((gateway) => gateway.id !== gatewayId);
  if (gateways.length === config.gateways.length) throw new Error("Server not found.");

  const activeGateway = gateways.find((gateway) => gateway.id === config.activeGatewayId) || gateways[0];
  return { gateways, activeGatewayId: activeGateway.id };
}

function normalizeConfig(config, idGenerator) {
  if (config && typeof config === "object") {
    if (Array.isArray(config.gateways)) {
      const gateways = config.gateways.map(normalizeGatewayOrNull).filter(Boolean);
      if (gateways.length > 0) {
        const activeGateway = gateways.find((gateway) => gateway.id === config.activeGatewayId) || gateways[0];
        return { gateways, activeGatewayId: activeGateway.id };
      }
    }

    const migratedUrl = normalizeGatewayUrl(config.gatewayUrl);
    if (migratedUrl) {
      const gateway = { id: idGenerator(), name: "Pi Server", url: migratedUrl };
      return { gateways: [gateway], activeGatewayId: gateway.id };
    }
  }

  const gateway = { id: idGenerator(), name: "Local", url: DEFAULT_GATEWAY_URL };
  return { gateways: [gateway], activeGatewayId: gateway.id };
}

function normalizeGateway(gateway) {
  const normalizedGateway = normalizeGatewayOrNull(gateway);
  if (!normalizedGateway) throw new Error("Enter an http or https URL.");
  return normalizedGateway;
}

function normalizeGatewayOrNull(gateway) {
  if (!gateway || typeof gateway !== "object") return null;

  const url = normalizeGatewayUrl(gateway.url);
  if (!url) return null;

  const id = String(gateway.id || "").trim();
  if (!id) return null;

  const name = String(gateway.name || "").trim() || new URL(url).host;
  return { id, name, url };
}

function normalizeGatewayUrl(candidate) {
  if (typeof candidate !== "string") return null;

  try {
    const url = new URL(candidate.trim());
    if (!["http:", "https:"].includes(url.protocol)) return null;
    return url.toString();
  } catch (_error) {
    return null;
  }
}

function readRawConfig(configPath) {
  try {
    return JSON.parse(fs.readFileSync(configPath, "utf8"));
  } catch (_error) {
    return null;
  }
}

module.exports = {
  DEFAULT_GATEWAY_URL,
  addGateway,
  normalizeGatewayUrl,
  readGatewayConfig,
  readOrCreateGatewayConfig,
  removeGateway,
  saveGateway,
  writeGatewayConfig
};
