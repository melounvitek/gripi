import { expect, test } from "@playwright/test";
import { message } from "../support/ui.mjs";

async function captureNotifications(page, transport, webPush = false) {
  await page.addInitScript(({ transport, webPush }) => {
    window.replyNotifications = [];
    document.hasFocus = () => false;
    localStorage.removeItem("gripi:notifications-disabled");
    if (transport === "desktop") {
      window.gripiElectron = { showNotification: async (notification) => window.replyNotifications.push(notification) };
    } else {
      delete window.PushManager;
      Object.defineProperty(window, "Notification", { value: { permission: "granted" } });
      const registration = { active: { postMessage: (notification) => window.replyNotifications.push(notification) } };
      if (webPush) {
        window.PushManager = class {};
        registration.pushManager = { getSubscription: async () => ({ toJSON: () => ({ endpoint: "https://push.test/subscription" }) }) };
      }
      Object.defineProperty(navigator, "serviceWorker", { value: {
        register: async () => registration,
        ready: Promise.resolve(registration),
      } });
    }
  }, { transport, webPush });
}

const assistant = (id, text, extra = {}) => ({ role: "assistant", id, content: [{ type: "text", text }], stopReason: "stop", ...extra });
const ended = (message) => ({ type: "message_end", message });
const agentEnd = (...messages) => ({ type: "agent_end", messages });
const settled = { type: "agent_settled" };
const bodies = (page) => page.evaluate(() => window.replyNotifications.map((notification) => notification.body));

async function liveEvents(page) {
  let pendingPayload = null;
  let seq = 0;
  await page.route(/\/events(?:\?|$)/, async (route) => {
    const payload = pendingPayload || { events: [], last_seq: seq, missed: false };
    pendingPayload = null;
    await route.fulfill({ json: payload });
  });
  await page.goto("/");
  await expect(page.locator("#live-output")).toBeAttached();
  return async (...events) => {
    const title = `Notification poll ${++seq}`;
    pendingPayload = {
      events: [...events, { type: "extension_ui_request", method: "setTitle", title }],
      last_seq: seq,
      missed: false,
    };
    await page.evaluate(() => window.dispatchEvent(new Event("pageshow")));
    await expect(page).toHaveTitle(title);
  };
}

const bell = (page) => page.getByRole("complementary", { name: "Sessions" }).getByRole("button", { name: /notifications/i });

for (const [state, { permission, disabled = false, name, color, slashed }] of Object.entries({
  "on": { permission: "granted", name: "Notifications on — click to disable", color: "rgb(255, 90, 31)", slashed: false },
  "off": { permission: "granted", disabled: true, name: "Notifications off — click to enable", color: "rgb(128, 128, 128)", slashed: true },
  "never enabled": { permission: "default", name: "Enable notifications", color: "rgb(128, 128, 128)", slashed: true },
  "blocked": { permission: "denied", name: "Notifications blocked — click for setup help", color: "rgb(204, 102, 102)", slashed: true },
})) {
  test(`the sidebar bell shows notifications ${state}`, async ({ page }) => {
    await page.addInitScript(({ permission, disabled }) => {
      if (disabled) localStorage.setItem("gripi:notifications-disabled", "true");
      else localStorage.removeItem("gripi:notifications-disabled");
      delete window.PushManager;
      Object.defineProperty(window, "Notification", { value: { permission } });
    }, { permission, disabled });
    await page.goto("/");
    await expect(bell(page)).toHaveAccessibleName(name);
    await expect(bell(page)).toHaveAttribute("title", name);
    await expect(bell(page)).toHaveCSS("color", color);
    await expect(bell(page).locator(".sidebar-tool-slash")).toBeVisible({ visible: slashed });
  });
}

test("the sidebar bell sits beside the hide button and toggles notifications", async ({ page }) => {
  await captureNotifications(page, "desktop");
  await page.goto("/");
  await expect(bell(page)).toHaveAccessibleName("Notifications on — click to disable");
  const bellBox = await bell(page).boundingBox();
  const hideBox = await page.getByRole("button", { name: "Hide sessions" }).boundingBox();
  expect([bellBox.width, bellBox.height]).toEqual([36, 36]);
  expect(bellBox.x + bellBox.width).toBeLessThanOrEqual(hideBox.x);
  expect(bellBox.y + bellBox.height / 2).toBeCloseTo(hideBox.y + hideBox.height / 2, 1);

  await bell(page).click();
  await expect(bell(page)).toHaveAccessibleName("Notifications off — click to enable");
  await bell(page).click();
  await expect(bell(page)).toHaveAccessibleName("Notifications on — click to disable");
});

test("active Web Push suppresses local settled notifications", async ({ page }) => {
  await captureNotifications(page, "browser", true);
  await page.route("**/web-push/config", (route) => route.fulfill({ json: { public_key: "AQID" } }));
  await page.route("**/web-push/subscription", (route) => route.fulfill({ json: {} }));
  const deliver = await liveEvents(page);
  await expect(bell(page)).toHaveAccessibleName("Notifications on — click to disable");
  const reply = assistant("push", "Delivered by Web Push");
  await deliver(ended(reply), agentEnd(reply));
  await deliver(settled);
  expect(await bodies(page)).toEqual([]);
});

for (const transport of ["desktop", "browser"]) {
  test(`${transport} notifies only the final settled reply across split polls`, async ({ page }) => {
    await captureNotifications(page, transport);
    const deliver = await liveEvents(page);
    const progress = assistant("progress", "Still working");
    const final = assistant(undefined, "**Final** feat/my_branch_name", { timestamp: 1700000000000, content: [
      { type: "text", text: "Private progress", textSignature: JSON.stringify({ v: 1, id: "progress", phase: "commentary" }) },
      { type: "text", text: "**Final** feat/my_branch_name" },
    ] });
    await deliver({ type: "agent_start" }, ended(progress));
    expect.soft(await bodies(page)).toEqual([]);
    await deliver(agentEnd(progress));
    expect.soft(await bodies(page)).toEqual([]);
    await deliver({ type: "agent_start" }, ended(final));
    expect.soft(await bodies(page)).toEqual([]);
    await deliver(agentEnd(progress, final, { role: "toolResult", content: "Tool result" }));
    expect.soft(await bodies(page)).toEqual([]);
    await deliver(settled);
    await expect.poll(() => bodies(page)).toEqual(["Final feat/my_branch_name"]);
    await deliver(settled, agentEnd(final), settled);
    expect(await bodies(page)).toEqual(["Final feat/my_branch_name"]);
    await deliver(agentEnd({ ...final, timestamp: final.timestamp + 1 }), settled);
    await expect.poll(() => bodies(page)).toEqual(["Final feat/my_branch_name", "Final feat/my_branch_name"]);
  });

  test(`${transport} replaces or clears each candidate without falling back to earlier replies`, async ({ page }) => {
    await captureNotifications(page, transport);
    const deliver = await liveEvents(page);
    const earlier = assistant("earlier", "Earlier eligible answer");
    const invalid = [
      assistant("aborted", "Aborted answer", { stopReason: "aborted" }),
      assistant("error", "Failed answer", { stopReason: "error" }),
      assistant("tool", "Tool preamble", { stopReason: "toolUse" }),
      assistant("unknown", "Unknown stop", { stopReason: "other" }),
      assistant("empty", ""),
      assistant("commentary", "", { content: [{ type: "text", text: "Only commentary", textSignature: JSON.stringify({ v: 1, id: "commentary", phase: "commentary" }) }] }),
    ];
    for (const last of invalid) {
      await deliver({ type: "agent_start" }, ended(earlier), agentEnd(earlier));
      await deliver(agentEnd(earlier, last), settled);
    }
    await deliver(agentEnd(earlier));
    await deliver(agentEnd({ role: "user", content: "No assistant" }), settled);
    await deliver(agentEnd(earlier));
    await deliver({ type: "agent_end" }, settled);
    await deliver(agentEnd(earlier));
    await deliver({ type: "agent_start" }, settled);
    expect(await bodies(page)).toEqual([]);

    for (const stopReason of [undefined, null, "", "stop", "length"]) {
      const reply = assistant(`allowed-${stopReason}`, `Allowed ${stopReason}`, { stopReason });
      await deliver(agentEnd(reply));
      await deliver(settled);
    }
    await expect.poll(() => bodies(page)).toEqual(["Allowed undefined", "Allowed null", "Allowed", "Allowed stop", "Allowed length"]);
  });

  test(`${transport} checks focus at settlement and clears candidates on replay recovery and view changes`, async ({ page }) => {
    await captureNotifications(page, transport);
    const deliver = await liveEvents(page);
    await deliver(agentEnd(assistant("focused", "Focused reply")));
    await page.evaluate(() => { document.hasFocus = () => true; });
    await deliver(settled);
    await page.evaluate(() => { document.hasFocus = () => false; });
    await deliver(settled);
    expect(await bodies(page)).toEqual([]);

    await deliver(agentEnd(assistant("disabled", "Disabled reply")));
    await page.evaluate(() => localStorage.setItem("gripi:notifications-disabled", "true"));
    await deliver(settled);
    await page.evaluate(() => localStorage.removeItem("gripi:notifications-disabled"));
    await deliver(settled);
    expect(await bodies(page)).toEqual([]);

    await deliver(agentEnd(assistant("missed", "Stale before recovery")));
    await page.route(/\/events(?:\?|$)/, async (route) => {
      await route.fulfill({ json: { events: [], last_seq: 100, missed: true } });
    }, { times: 1 });
    const previousTitle = await page.title();
    const refreshed = page.waitForResponse(/\/session_fragment(?:\?|$)/);
    await page.evaluate(() => window.dispatchEvent(new Event("pageshow")));
    await refreshed;
    await expect(page).not.toHaveTitle(previousTitle);
    await deliver(settled);
    expect(await bodies(page)).toEqual([]);

    await deliver(agentEnd(assistant("switched", "Stale before switching")));
    const otherSession = page.locator("a.session:not(.selected)").first();
    const path = await otherSession.getAttribute("data-session-path");
    await otherSession.click();
    await expect(page.locator('.prompt-form input[name="session"]')).toHaveValue(path);
    await deliver(settled);
    expect(await bodies(page)).toEqual([]);
  });

  test(`${transport} live reply notifications stay quiet during external follow without takeover catch-up`, async ({ page }) => {
    await captureNotifications(page, transport);
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
      const reply = assistant(`notification-${++seq}`, text);
      pendingPayload = {
        events: [ended(reply), agentEnd(reply), settled],
        last_seq: seq,
        missed: false,
        session_sync: { mode, revision: "notification-test", gateway_busy: false },
      };
      await page.evaluate(() => window.dispatchEvent(new Event("pageshow")));
    }

    // Incoming sync mode must take effect before this batch can notify.
    await deliverReply("CLI reply while entering follow mode");
    await expect(page.locator("#live-output")).toHaveAttribute("data-session-sync-mode", "external_follow");
    expect.soft(await bodies(page)).toEqual([]);

    await deliverReply("CLI reply while already following");
    await expect(message(page, "assistant", "CLI reply while already following")).toBeVisible();
    expect.soft(await bodies(page)).toEqual([]);

    mode = "managed";
    await deliverReply("CLI reply arriving with takeover");
    await expect(page.locator("#live-output")).toHaveAttribute("data-session-sync-mode", "managed");
    expect.soft(await bodies(page)).toEqual([]);

    await deliverReply("New gateway reply after takeover");
    await expect(message(page, "assistant", "New gateway reply after takeover")).toBeVisible();
    await expect.poll(() => bodies(page)).toEqual(["New gateway reply after takeover"]);
  });
}
