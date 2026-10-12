const ALLOWED_PERMISSIONS = new Set(["notifications", "clipboard-sanitized-write"]);

// The gateway may notify, write to the clipboard and use the microphone for voice conversations; nothing needs the camera.
function installGatewayPermissionHandlers(gatewaySession, allowedOrigin) {
  gatewaySession.setPermissionCheckHandler((_webContents, permission, requestingOrigin, details) => {
    return allowsGatewayPermission(permission, requestingOrigin, details.mediaType ? [details.mediaType] : [], allowedOrigin);
  });
  gatewaySession.setPermissionRequestHandler((_webContents, permission, callback, details) => {
    callback(allowsGatewayPermission(permission, details.requestingUrl, details.mediaTypes ?? [], allowedOrigin));
  });
}

// Electron passes both handlers URLs, such as https://gripi.example.test/, rather than bare origins.
function allowsGatewayPermission(permission, requestingUrl, mediaTypes, allowedOrigin) {
  if (URL.parse(requestingUrl)?.origin !== allowedOrigin) return false;
  if (permission === "media") return mediaTypes.length > 0 && mediaTypes.every((type) => type === "audio");
  return ALLOWED_PERMISSIONS.has(permission);
}

module.exports = { installGatewayPermissionHandlers };
