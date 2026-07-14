const { normalizeGatewayUrl } = require("./gateway_config");

const DEFAULT_GATEWAY_URL = "http://localhost:4567/";

function gatewayUrl(env = process.env, argv = process.argv, configuredUrl = null) {
  const candidate = urlFromArgs(argv) || env.GRIPI_DESKTOP_URL || configuredUrl || DEFAULT_GATEWAY_URL;
  return normalizeGatewayUrl(candidate) || DEFAULT_GATEWAY_URL;
}

function urlFromArgs(argv) {
  const prefix = "--gateway-url=";
  const match = argv.find((arg) => arg.startsWith(prefix));
  return match ? match.slice(prefix.length) : null;
}

module.exports = { DEFAULT_GATEWAY_URL, gatewayUrl };
