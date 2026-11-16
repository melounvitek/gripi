import { expect, test as base } from "@playwright/test";
import { execFile } from "node:child_process";
import { randomUUID } from "node:crypto";
import { readFile, unlink, writeFile } from "node:fs/promises";
import path from "node:path";
import { promisify } from "node:util";
import { sessions } from "../support/contract.mjs";

const run = promisify(execFile);

const test = base.extend({
  // A conversation and a worker that gripi link put under it, both copies of the seed session.
  linked: async ({ page }, use) => {
    await page.goto("/?show_all_sessions=1");
    const seedLink = page.getByRole("link", { name: new RegExp(sessions.marker) });
    const seedPath = new URL(await seedLink.getAttribute("href"), page.url()).searchParams.get("session");
    const entries = (await readFile(seedPath, "utf8")).trim().split("\n").map(JSON.parse);
    const copies = [];
    for (const role of ["Parent", "Worker"]) {
      const id = randomUUID();
      const file = path.join(path.dirname(seedPath), `${role.toLowerCase()}-${id}.jsonl`);
      const title = `E2E ${role} ${id.slice(0, 8)}`;
      entries[0].id = id;
      entries.find((entry) => entry.type === "session_info").name = title;
      await writeFile(file, `${entries.map((entry) => JSON.stringify(entry)).join("\n")}\n`);
      copies.push({ file, title });
    }
    const [parent, worker] = copies;
    try {
      const env = { ...process.env, GRIPI_SOCKET_PATH: process.env.GRIPI_E2E_SOCKET_PATH };
      // gripi link finds the sessions once the gateway has listed the new files.
      await expect(async () => { await run(process.env.GRIPI_E2E_GRIPI, ["link", worker.file, parent.file], { env }); }).toPass();
      await use({ parent, worker });
    } finally {
      await Promise.all(copies.map(({ file }) => unlink(file)));
    }
  },
});

for (const touch of [false, true]) {
  test.describe(touch ? "mobile touch" : "desktop", () => {
    test.use(touch ? { viewport: { width: 393, height: 851 }, isMobile: true, hasTouch: true } : {});

    test("a linked session is a compact line under its parent that opens on the first tap", async ({ page, linked }, testInfo) => {
      await page.goto("/?show_all_sessions=1");
      if (touch) {
        await page.locator('label[aria-label="Open sessions"]').tap();
        // Row hit tests need the drawer to have finished sliding in.
        await expect(page.locator(".session-sidebar")).toHaveCSS("transform", "matrix(1, 0, 0, 1, 0, 0)");
      }
      const parentRow = page.locator(`.session-row[data-session-path="${linked.parent.file}"]`);
      const workerRow = page.locator(`.session-row[data-session-path="${linked.worker.file}"]`);
      await expect(workerRow).toHaveClass(/\bis-worker\b/);
      expect(await parentRow.evaluate((row) => row.nextElementSibling?.dataset.sessionPath)).toBe(linked.worker.file);
      await expect(workerRow.locator(".session-project")).toBeHidden();
      // Lower than a normal row, yet still easy to tap on a phone.
      const [workerBox, parentBox] = await Promise.all([workerRow.boundingBox(), parentRow.boundingBox()]);
      expect(workerBox.height).toBeCloseTo(touch ? 40 : 28.8, 0);
      expect(workerBox.height).toBeLessThan(parentBox.height);
      expect(workerBox.x).toBeGreaterThan(parentBox.x);
      await workerRow.scrollIntoViewIfNeeded();
      await page.screenshot({ path: testInfo.outputPath("linked-session.png") });

      const link = workerRow.locator("a.session");
      await (touch ? link.tap() : link.click());
      await expect(page.getByRole("heading", { level: 1, name: linked.worker.title })).toBeVisible();
    });
  });
}

test("an open sub-session does not notify of its replies", async ({ page, linked }) => {
  await page.addInitScript(() => {
    window.replyNotifications = [];
    document.hasFocus = () => false;
    localStorage.removeItem("gripi:notifications-disabled");
    window.gripiElectron = { showNotification: async (notification) => window.replyNotifications.push(notification) };
  });
  let pending = null;
  let seq = 0;
  await page.route(/\/events(?:\?|$)/, (route) => {
    const payload = pending || { events: [], last_seq: seq, missed: false };
    pending = null;
    return route.fulfill({ json: payload });
  });
  const notified = [];
  for (const session of [linked.worker, linked.parent]) {
    await page.goto(`/?session=${encodeURIComponent(session.file)}`);
    await expect(page.locator("#live-output")).toBeAttached();
    const reply = { role: "assistant", id: `reply-${seq}`, content: [{ type: "text", text: `Reply in ${session.title}` }], stopReason: "stop" };
    const title = `Notification poll ${++seq}`;
    pending = { events: [{ type: "message_end", message: reply }, { type: "agent_end", messages: [reply] }, { type: "agent_settled" }, { type: "extension_ui_request", method: "setTitle", title }], last_seq: seq, missed: false };
    await page.evaluate(() => window.dispatchEvent(new Event("pageshow")));
    await expect(page).toHaveTitle(title);
    // The reply is settled before the title changes, so its notification has been shown by now.
    notified.push(...await page.evaluate(() => window.replyNotifications.map((notification) => notification.body)));
  }
  expect(notified).toEqual([`Reply in ${linked.parent.title}`]);
});
