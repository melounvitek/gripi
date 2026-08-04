import { nativeBridgeMethod, nativeNotificationsRequirePermission } from "./native_bridge.js";
import { WebPushController } from "./web_push.js";

const enableButton = document.querySelector("[data-enable]");
const sendButton = document.querySelector("[data-send]");
const statusBox = document.querySelector("[data-status]");
const webPushController = new WebPushController(window, navigator);

function setStatus(message, error = false) {
  statusBox.textContent = message;
  statusBox.classList.toggle("error", error);
}

function nativeNotificationAvailable() {
  return Boolean(nativeBridgeMethod(window, "showNotification"));
}

function notificationAvailable() {
  return nativeNotificationAvailable() || webPushController.available();
}

async function refreshState() {
  if (!notificationAvailable()) {
    enableButton.disabled = true;
    sendButton.disabled = true;
    setStatus("This browser context does not support Web Push. On iPhone, install Gripi on the Home Screen before enabling notifications.", true);
    return;
  }

  if (nativeNotificationAvailable()) {
    const permission = await nativeBridgeMethod(window, "notificationPermission")?.();
    const enabled = !nativeNotificationsRequirePermission(window) || permission?.ok;
    sendButton.disabled = !enabled;
    if (permission?.status === "denied") {
      setStatus("App notification permission is denied. Tap Enable notifications to open iOS Settings.", true);
    } else {
      setStatus(enabled ? "App notifications are available. Tap Send test notification." : "Tap Enable notifications to grant app permission.");
    }
  } else if (Notification.permission === "granted") {
    const enabled = await webPushController.reconcile();
    sendButton.disabled = !enabled;
    setStatus(enabled ? "Web Push is enabled. You can close Gripi and send a test notification." : "Web Push could not be enabled.", !enabled);
  } else if (Notification.permission === "denied") {
    enableButton.disabled = true;
    sendButton.disabled = true;
    setStatus("Notification permission is denied for this app/site. Re-enable it in system notification settings, then try again.", true);
  } else {
    sendButton.disabled = true;
    setStatus("Permission has not been requested yet. Tap Enable notifications.");
  }
}

enableButton.addEventListener("click", async () => {
  try {
    if (!notificationAvailable()) {
      await refreshState();
      return;
    }
    if (nativeNotificationAvailable()) {
      if (nativeNotificationsRequirePermission(window)) {
        setStatus("Requesting notification permission…");
        await nativeBridgeMethod(window, "requestNotificationPermission")?.();
      }
      await refreshState();
      return;
    }
    setStatus("Requesting notification permission…");
    const enabled = await webPushController.enable();
    if (!enabled && Notification.permission !== "denied") throw new Error("Web Push could not be enabled.");
    await refreshState();
  } catch (error) {
    setStatus(`Enable failed: ${error.message || error}`, true);
  }
});

sendButton.addEventListener("click", async () => {
  try {
    if (nativeNotificationAvailable()) {
      const result = await nativeBridgeMethod(window, "showNotification")({
        type: "gripi-notification-test",
        title: "Gripi test",
        body: "If you can see this, app notifications work here.",
        tag: "gripi-notification-test",
        url: "/notification-test"
      });
      if (!result?.ok) throw new Error("App notification bridge did not accept the request.");
    } else {
      const response = await fetch("/web-push/test", { method: "POST" });
      if (!response.ok) throw new Error(`Web Push test failed (${response.status}).`);
    }
    setStatus(nativeNotificationAvailable() ? "Test notification sent. Closed-app delivery will be enabled with the upcoming APNs gateway integration." : "Test notification sent. You can close Gripi; Web Push does not require this page to remain open.");
  } catch (error) {
    setStatus(`Send failed: ${error.message || error}`, true);
  }
});

window.addEventListener("focus", () => refreshState().catch(() => {}));
webPushController.prepare().catch(() => {});
refreshState().catch((error) => setStatus(`Setup failed: ${error.message || error}`, true));
