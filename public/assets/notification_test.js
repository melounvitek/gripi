import { WebPushController } from "./web_push.js";

const enableButton = document.querySelector("[data-enable]");
const sendButton = document.querySelector("[data-send]");
const statusBox = document.querySelector("[data-status]");
const webPushController = new WebPushController(window, navigator);

function setStatus(message, error = false) {
  statusBox.textContent = message;
  statusBox.classList.toggle("error", error);
}

function desktopNotificationAvailable() {
  return Boolean(window.gripiElectron?.showNotification);
}

function notificationAvailable() {
  return desktopNotificationAvailable() || webPushController.available();
}

async function refreshState() {
  if (!notificationAvailable()) {
    enableButton.disabled = true;
    sendButton.disabled = true;
    setStatus("This browser context does not support Web Push. On iPhone, install Gripi on the Home Screen before enabling notifications.", true);
    return;
  }

  if (desktopNotificationAvailable()) {
    sendButton.disabled = false;
    setStatus("Desktop notifications are available. Tap Send test notification.");
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
    if (!notificationAvailable() || desktopNotificationAvailable()) {
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
    if (desktopNotificationAvailable()) {
      const result = await window.gripiElectron.showNotification({
        type: "gripi-notification-test",
        title: "Gripi test",
        body: "If you can see this, desktop notifications work here.",
        tag: "gripi-notification-test",
        url: "/notification-test"
      });
      if (!result?.ok) throw new Error("Desktop notification bridge did not accept the request.");
    } else {
      const response = await fetch("/web-push/test", { method: "POST" });
      if (!response.ok) throw new Error(`Web Push test failed (${response.status}).`);
    }
    setStatus("Test notification sent. You can close Gripi; Web Push does not require this page to remain open.");
  } catch (error) {
    setStatus(`Send failed: ${error.message || error}`, true);
  }
});

webPushController.prepare().catch(() => {});
refreshState().catch((error) => setStatus(`Setup failed: ${error.message || error}`, true));
