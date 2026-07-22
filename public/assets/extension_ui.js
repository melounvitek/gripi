export function extensionUiRequestExpired(request, now = Date.now()) {
  return !!(request?.expiresAt && request.expiresAt <= now);
}

export function extensionUiResponseDisposition(response) {
  if ([404, 422].includes(response.status)) return "definitive-rejection";
  return response.ok ? "accepted" : "retry";
}
