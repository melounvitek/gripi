(function installWebPushWorker(scope) {
  const SHOWN_SESSION_TIMEOUT_MS = 1000;

  function notificationSession(url, origin) {
    try {
      return new URL(url || "/", origin).searchParams.get("session") || "";
    } catch (_error) {
      return "";
    }
  }

  // Asks a page which session it shows, which its URL may not name, such as at /, where the installed app starts, or
  // not anymore, such as once a new session got its own key.
  function shownSession(client) {
    return new Promise((resolve) => {
      const { port1, port2 } = new MessageChannel();
      const answer = (shown) => {
        clearTimeout(timer);
        port1.close();
        resolve(typeof shown === "string" ? shown : "");
      };
      // A page from before Gripi asked never answers.
      const timer = setTimeout(answer, SHOWN_SESSION_TIMEOUT_MS);
      port1.onmessage = (event) => answer(event.data);
      client.postMessage({ type: "gripi-shown-session" }, [port2]);
    });
  }

  async function targetSessionFocused(data, worker) {
    if (data.type !== "gripi-notification") return false;
    // The URL of a Hermes conversation, or a long one, doesn't name the session, which the notification may.
    const targetSession = data.session || notificationSession(data.url, worker.location.origin);
    if (!targetSession) return false;

    const clientList = await worker.clients.matchAll({ type: "window", includeUncontrolled: true });
    const shown = await Promise.all(clientList.filter((client) => client.focused).map(shownSession));
    return shown.includes(targetSession);
  }

  scope.gripiDisplayPushNotification = async function displayPushNotification(data, worker) {
    if (await targetSessionFocused(data, worker)) return false;

    const test = data.type === "gripi-notification-test";
    await worker.registration.showNotification(data.title || "Gripi", {
      body: data.plain ? data.body : scope.gripiNotificationReplyPreview(data.body || "Notifications are working."),
      tag: data.tag || (test ? "gripi-notification-test" : "gripi-notification"),
      renotify: false,
      icon: "/app-icon.svg",
      badge: "/assets/notification-badge.png",
      data: { url: data.url || (test ? "/notification-test" : "/") }
    });
    return true;
  };
})(globalThis);
