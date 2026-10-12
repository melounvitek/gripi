const ALLOWED_PERMISSIONS = new Set(["notifications", "clipboard-sanitized-write"]);

// The gateway may notify, write to the clipboard and use the microphone for voice conversations; nothing needs the camera.
function allowsGatewayPermission(permission, requestingOrigin, mediaTypes, allowedOrigin) {
  if (requestingOrigin !== allowedOrigin) return false;
  if (permission === "media") return mediaTypes.length > 0 && mediaTypes.every((type) => type === "audio");
  return ALLOWED_PERMISSIONS.has(permission);
}

module.exports = { allowsGatewayPermission };
