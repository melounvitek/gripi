export function nativeBridgeMethod(windowObject, methodName) {
  for (const bridge of [windowObject?.gripiNative, windowObject?.gripiElectron]) {
    if (typeof bridge?.[methodName] === "function") return bridge[methodName].bind(bridge);
  }
  return null;
}

export function nativeNotificationsRequirePermission(windowObject) {
  return Boolean(windowObject?.gripiNative?.notificationsRequirePermission);
}
