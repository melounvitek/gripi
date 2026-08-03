(function installWebPushWorker(scope) {
  function notificationSession(url, origin) {
    try {
      return new URL(url || "/", origin).searchParams.get("session") || "";
    } catch (_error) {
      return "";
    }
  }

  async function targetSessionFocused(data, worker) {
    if (data.type !== "gripi-notification") return false;
    const targetSession = notificationSession(data.url, worker.location.origin);
    if (!targetSession) return false;

    const clientList = await worker.clients.matchAll({ type: "window", includeUncontrolled: true });
    return clientList.some((client) => client.focused && notificationSession(client.url, worker.location.origin) === targetSession);
  }

  scope.gripiDisplayPushNotification = async function displayPushNotification(data, worker) {
    if (await targetSessionFocused(data, worker)) return false;

    const test = data.type === "gripi-notification-test";
    await worker.registration.showNotification(data.title || "Gripi", {
      body: scope.gripiNotificationReplyPreview(data.body || "Notifications are working."),
      tag: data.tag || (test ? "gripi-notification-test" : "gripi-notification"),
      renotify: false,
      icon: "/app-icon.svg",
      badge: "/app-icon.svg",
      data: { url: data.url || (test ? "/notification-test" : "/") }
    });
    return true;
  };
})(globalThis);
