import { expect, test as base } from "@playwright/test";
import { execFile } from "node:child_process";
import { randomUUID } from "node:crypto";
import { readFile, unlink, writeFile } from "node:fs/promises";
import path from "node:path";
import { promisify } from "node:util";
import { prompts, replies, sessions } from "../support/contract.mjs";
import { expectRunFinished, sendPrompt } from "../support/ui.mjs";

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

    test("an unread sub-session stays listed in bold under its parent until it is opened", async ({ page, linked }) => {
      const [unread] = linked.workers;
      // Unlike its parent's, a message of the user's own leaves a reply to read.
      await gripi("send", unread.file, prompts.standard);
      await expect(async () => {
        const { stdout } = await gripi("list", "--json");
        expect(JSON.parse(stdout).find((session) => session.path === unread.file)).toMatchObject({ state: "idle", unread: true });
      }).toPass();
      await openSidebar(page, linked.parent);
      const link = row(page, unread).locator("a.session");
      expect(await row(page, linked.parent).evaluate((element) => element.nextElementSibling?.dataset.sessionPath)).toBe(unread.file);
      await expect(link).toHaveClass(/\bunread\b/);
      await expect(link.locator(".session-title")).toHaveCSS("font-weight", "700");
      await expect(fold(page, linked.parent).locator("summary")).toHaveText("2 sub-sessions done");

      await press(link);
      await expect(page.getByRole("heading", { level: 1, name: unread.title })).toBeVisible();
      await openSidebar(page, linked.parent);
      await expect(fold(page, linked.parent).locator("summary")).toHaveText("3 sub-sessions done");
      await expect(row(page, unread)).toBeHidden();
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

test("an open sub-session notifies of the user's own turns, not of those that its parent started or joined", async ({ page, linked }) => {
  await page.addInitScript(() => {
    window.replyNotifications = [];
    document.hasFocus = () => false;
    localStorage.removeItem("gripi:notifications-disabled");
    window.gripiElectron = { showNotification: async (notification) => window.replyNotifications.push(notification) };
  });
  const [open] = linked.workers;
  // As the parent's bash tool runs it, gripi sends as the parent, which gets a report of the turn instead.
  const fromParent = (...args) => run(process.env.GRIPI_E2E_GRIPI, args, { env: { ...process.env, GRIPI_SOCKET_PATH: process.env.GRIPI_E2E_SOCKET_PATH, PI_SESSION_FILE: linked.parent.file } });
  // The sidebar notifies of the turns that the gateway finished since it last drew, and focus redraws it at once.
  const notified = (session) => page.evaluate((tag) => {
    window.dispatchEvent(new Event("focus"));
    return window.replyNotifications.filter((notification) => notification.tag === tag).map((notification) => notification.body);
  }, `gripi-final-reply:${session.file}`);
  await page.goto(`/?session=${encodeURIComponent(open.file)}`);
  await expect(page.locator("#live-output")).toBeAttached();

  // The parent's reply to the report notifies, so the turn has ended by then.
  await fromParent("send", open.file, prompts.standard);
  await expect.poll(() => notified(linked.parent)).toHaveLength(1);
  await expectRunFinished(page);

  // A turn that the page started is the parent's too once the parent joins it.
  await sendPrompt(page, prompts.followUpStart);
  await expect(async () => {
    const { stdout } = await gripi("list", "--json");
    expect(JSON.parse(stdout).find((session) => session.path === open.file)).toMatchObject({ state: "working" });
  }).toPass();
  await fromParent("send", open.file, prompts.followUpMessage);
  await expect.poll(() => notified(linked.parent)).toHaveLength(2);
  await expectRunFinished(page);

  // Only the user's own turn notifies.
  await sendPrompt(page, prompts.standard);
  await expect.poll(() => notified(open)).toEqual([replies.standard]);
});
