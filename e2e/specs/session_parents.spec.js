import { expect, test as base } from "@playwright/test";
import { execFile } from "node:child_process";
import { randomUUID } from "node:crypto";
import { readFile, unlink, writeFile } from "node:fs/promises";
import path from "node:path";
import { promisify } from "node:util";
import { prompts, sessions } from "../support/contract.mjs";

const run = promisify(execFile);
const gripi = (...args) => run(process.env.GRIPI_E2E_GRIPI, args, { env: { ...process.env, GRIPI_SOCKET_PATH: process.env.GRIPI_E2E_SOCKET_PATH } });

const test = base.extend({
  // A conversation and three sub-sessions that gripi link put under it, all copies of the seed session. They are
  // recent, so that the sidebar lists them.
  linked: async ({ page }, use) => {
    await page.goto("/?show_all_sessions=1");
    const seedLink = page.getByRole("link", { name: new RegExp(sessions.marker) });
    const seedPath = new URL(await seedLink.getAttribute("href"), page.url()).searchParams.get("session");
    const entries = (await readFile(seedPath, "utf8")).trim().split("\n").map(JSON.parse);
    for (const entry of entries) {
      if (entry.timestamp) entry.timestamp = new Date().toISOString();
      if (typeof entry.message?.timestamp === "number") entry.message.timestamp = Date.now();
    }
    const copies = [];
    for (const role of ["Parent", "First", "Second", "Third"]) {
      const id = randomUUID();
      const file = path.join(path.dirname(seedPath), `${role.toLowerCase()}-${id}.jsonl`);
      const title = `E2E ${role} ${id.slice(0, 8)}`;
      entries[0].id = id;
      entries.find((entry) => entry.type === "session_info").name = title;
      await writeFile(file, `${entries.map((entry) => JSON.stringify(entry)).join("\n")}\n`);
      copies.push({ file, title });
    }
    const [parent, ...workers] = copies;
    try {
      // gripi link finds the sessions once the gateway has listed the new files.
      for (const worker of workers) await expect(async () => { await gripi("link", worker.file, parent.file); }).toPass();
      await use({ parent, workers });
    } finally {
      await Promise.all(copies.map(({ file }) => unlink(file)));
    }
  },
});

for (const touch of [false, true]) {
  test.describe(touch ? "mobile touch" : "desktop", () => {
    test.use(touch ? { viewport: { width: 393, height: 851 }, isMobile: true, hasTouch: true } : {});

    const press = (locator) => (touch ? locator.tap() : locator.click());
    async function openSidebar(page, session) {
      await page.goto(`/?session=${encodeURIComponent(session.file)}`);
      if (touch) {
        await page.locator('label[aria-label="Open sessions"]').tap();
        // Row hit tests need the drawer to have finished sliding in.
        await expect(page.locator(".session-sidebar")).toHaveCSS("transform", "matrix(1, 0, 0, 1, 0, 0)");
      }
    }
    const row = (page, session) => page.locator(`.session-row[data-session-path="${session.file}"]`);
    const fold = (page, session) => page.locator(`details[data-worker-fold="${session.file}"]`);

    test("done sub-sessions fold into a line under their parent that opens on the first tap and stays open", async ({ page, linked }, testInfo) => {
      await page.clock.install();
      await openSidebar(page, linked.parent);
      const parentRow = row(page, linked.parent);
      const summary = fold(page, linked.parent).locator("summary");
      expect(await parentRow.evaluate((element) => element.nextElementSibling?.dataset.workerFold)).toBe(linked.parent.file);
      await expect(summary).toHaveText("3 sub-sessions done");
      for (const worker of linked.workers) await expect(row(page, worker)).toBeHidden();

      await press(summary);
      for (const worker of linked.workers) await expect(row(page, worker)).toBeVisible();
      const workerRow = row(page, linked.workers[0]);
      await expect(workerRow).toHaveClass(/\bis-worker\b/);
      await expect(workerRow.locator(".session-project")).toBeHidden();
      // Lower than a normal row, yet still easy to tap on a phone.
      const [workerBox, summaryBox, parentBox] = await Promise.all([workerRow.boundingBox(), summary.boundingBox(), parentRow.boundingBox()]);
      expect(workerBox.height).toBeCloseTo(touch ? 40 : 28.8, 0);
      expect(summaryBox.height).toBeCloseTo(workerBox.height, 0);
      expect(workerBox.height).toBeLessThan(parentBox.height);
      expect(workerBox.x).toBeGreaterThan(parentBox.x);

      // The sidebar redraws itself every ten seconds.
      await fold(page, linked.parent).evaluate((element) => { element.dataset.drawnBefore = ""; });
      await page.clock.runFor(11_000);
      await expect(page.locator("details[data-drawn-before]")).toHaveCount(0);
      await expect(fold(page, linked.parent)).toHaveAttribute("open", "");
      await workerRow.scrollIntoViewIfNeeded();
      await page.screenshot({ path: testInfo.outputPath("sub-sessions.png") });

      // Opening one redraws the sidebar too.
      await press(workerRow.locator("a.session"));
      await expect(page.getByRole("heading", { level: 1, name: linked.workers[0].title })).toBeVisible();
      await expect(fold(page, linked.parent)).toHaveAttribute("open", "");
    });

    test("the open sub-session stays listed though it is done", async ({ page, linked }) => {
      const [open] = linked.workers;
      await openSidebar(page, open);
      await expect(row(page, open).locator("a.session.selected")).toBeVisible();
      expect(await row(page, linked.parent).evaluate((element) => element.nextElementSibling?.dataset.sessionPath)).toBe(open.file);
      await expect(fold(page, linked.parent).locator("summary")).toHaveText("2 sub-sessions done");
    });

    test("a running sub-session stays listed above the fold", async ({ page, linked }) => {
      const running = linked.workers[2];
      // It runs until someone answers its dialog.
      await gripi("send", running.file, prompts.extension);
      try {
        await openSidebar(page, linked.parent);
        await expect(row(page, running).getByRole("img", { name: "Pi is working" })).toBeVisible();
        expect(await row(page, linked.parent).evaluate((element) => element.nextElementSibling?.dataset.sessionPath)).toBe(running.file);
        await expect(fold(page, linked.parent).locator("summary")).toHaveText("2 sub-sessions done");
      } finally {
        await gripi("stop", running.file);
      }
    });

    test("a pinned parent folds its done sub-sessions too", async ({ page, linked }) => {
      await gripi("pin", linked.parent.file);
      try {
        await openSidebar(page, linked.parent);
        const summary = page.locator(".pinned-sessions-list").locator(`details[data-worker-fold="${linked.parent.file}"] summary`);
        await expect(summary).toHaveText("3 sub-sessions done");
        await press(summary);
        for (const worker of linked.workers) await expect(row(page, worker)).toBeVisible();
      } finally {
        await gripi("unpin", linked.parent.file);
      }
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
  for (const session of [linked.workers[0], linked.parent]) {
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
