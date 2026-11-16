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
