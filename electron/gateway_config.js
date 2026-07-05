const fs = require("node:fs");
const path = require("node:path");

function readGatewayUrl(configPath) {
  try {
    const config = JSON.parse(fs.readFileSync(configPath, "utf8"));
    return normalizeGatewayUrl(config.gatewayUrl);
  } catch (_error) {
    return null;
  }
}

function writeGatewayUrl(configPath, gatewayUrl) {
  const normalizedUrl = normalizeGatewayUrl(gatewayUrl);
  if (!normalizedUrl) throw new Error("Enter an http or https URL.");

  fs.mkdirSync(path.dirname(configPath), { recursive: true });
  fs.writeFileSync(configPath, `${JSON.stringify({ gatewayUrl: normalizedUrl }, null, 2)}\n`);
  return normalizedUrl;
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

module.exports = { normalizeGatewayUrl, readGatewayUrl, writeGatewayUrl };
