import { expect, test } from "@playwright/test";
import { message } from "../support/ui.mjs";

for (const transport of ["desktop", "browser"]) {
  test(`${transport} live reply notifications stay quiet during external follow without takeover catch-up`, async ({ page }) => {
    await page.addInitScript((transport) => {
      window.replyNotifications = [];
      document.hasFocus = () => false;
      localStorage.removeItem("gripi:notifications-disabled");
      if (transport === "desktop") {
        window.gripiElectron = { showNotification: async (notification) => window.replyNotifications.push(notification) };
      } else {
        delete window.PushManager;
        Object.defineProperty(window, "Notification", { value: { permission: "granted" } });
        const registration = { active: { postMessage: (notification) => window.replyNotifications.push(notification) } };
        Object.defineProperty(navigator, "serviceWorker", { value: {
          register: async () => registration,
          ready: Promise.resolve(registration),
        } });
      }
    }, transport);

    let pendingPayload = null;
    let mode = "external_follow";
    await page.route(/\/events(?:\?|$)/, async (route) => {
      const payload = pendingPayload || { events: [], last_seq: 0, missed: false };
      pendingPayload = null;
      await route.fulfill({ json: payload });
    });
    await page.route(/\/session_fragment(?:\?|$)/, async (route) => {
      const response = await route.fetch();
      const payload = await response.json();
      payload.conversation_html = payload.conversation_html
        .replace(/data-session-sync-mode="[^"]*"/, `data-session-sync-mode="${mode}"`)
        .replace(/data-session-sync-revision="[^"]*"/, 'data-session-sync-revision="notification-test"');
      await route.fulfill({ json: payload });
    });
    await page.goto("/");
    await expect(page.locator("#live-output")).toBeAttached();

    let seq = 0;
    async function deliverReply(text) {
      seq += 1;
      pendingPayload = {
        events: [{ type: "message_end", message: { role: "assistant", id: `notification-${seq}`, content: [{ type: "text", text }], stopReason: "stop" } }],
        last_seq: seq,
        missed: false,
        session_sync: { mode, revision: "notification-test", gateway_busy: false },
      };
      await page.evaluate(() => window.dispatchEvent(new Event("pageshow")));
    }

    // Incoming sync mode must take effect before this batch can notify.
    await deliverReply("CLI reply while entering follow mode");
    await expect(page.locator("#live-output")).toHaveAttribute("data-session-sync-mode", "external_follow");
    expect.soft(await page.evaluate(() => window.replyNotifications)).toEqual([]);

    await deliverReply("CLI reply while already following");
    await expect(message(page, "assistant", "CLI reply while already following")).toBeVisible();
    expect.soft(await page.evaluate(() => window.replyNotifications)).toEqual([]);

    mode = "managed";
    await deliverReply("CLI reply arriving with takeover");
    await expect(page.locator("#live-output")).toHaveAttribute("data-session-sync-mode", "managed");
    expect.soft(await page.evaluate(() => window.replyNotifications)).toEqual([]);

    await deliverReply("New gateway reply after takeover");
    await expect(message(page, "assistant", "New gateway reply after takeover")).toBeVisible();
    await expect.poll(() => page.evaluate(() => window.replyNotifications.map((notification) => notification.body)))
      .toEqual(["New gateway reply after takeover"]);
  });
}
