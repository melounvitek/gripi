import { expect, test as base } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { readFile, unlink, writeFile } from "node:fs/promises";
import path from "node:path";
import { sessions } from "../support/contract.mjs";
import { message } from "../support/ui.mjs";

const test = base.extend({
  // A copy of a seeded session whose folder has since been deleted.
  orphan: async ({ page }, use) => {
    await page.goto("/?show_all_sessions=1");
    const seedURL = new URL(await page.getByRole("link", { name: new RegExp(sessions.marker) }).getAttribute("href"), page.url());
    const seedPath = seedURL.searchParams.get("session");
    const entries = (await readFile(seedPath, "utf8")).trim().split("\n").map(JSON.parse);
    const id = randomUUID();
    const file = path.join(path.dirname(seedPath), `missing-folder-${id}.jsonl`);
    const title = `E2E Missing Folder ${id.slice(0, 8)}`;
    const folder = path.join(path.dirname(entries[0].cwd), `deleted-${id.slice(0, 8)}`);
    entries[0] = { ...entries[0], id, cwd: folder };
    entries.find((entry) => entry.type === "session_info").name = title;
    await writeFile(file, `${entries.map((entry) => JSON.stringify(entry)).join("\n")}\n`);
    seedURL.searchParams.set("show_all_sessions", "1");
    try {
      await use({ file, title, folder, seedURL: seedURL.href });
    } finally {
      await unlink(file);
    }
  },
});

for (const touch of [false, true]) {
  test.describe(touch ? "mobile touch" : "desktop", () => {
    // Keep the touch case beside the desktop one despite the mobile project's restricted testMatch.
    test.use(touch ? { viewport: { width: 393, height: 851 }, isMobile: true, hasTouch: true } : {});

    test("a session whose folder is gone stays readable with sending paused", async ({ page, orphan }) => {
      await page.goto(orphan.seedURL);
      await openSidebar(page, touch);
      const row = page.locator(`.session-row[data-session-path="${orphan.file}"]`);
      await expect(row.locator(".session-meta")).toHaveText("no folder");
      await expect(row.locator(".session-folder-missing")).toHaveAttribute("title", `Folder ${orphan.folder} no longer exists`);

      // The first tap opens it, and the in-page switch must leave the composer disabled.
      const link = row.locator("a.session");
      await (touch ? link.tap() : link.click());
      await expect(page.getByRole("heading", { level: 1, name: orphan.title })).toBeVisible();
      await expect(message(page, "assistant", "The external E2E target is disposable.")).toBeVisible();
      const banner = page.locator("[data-session-sync-banner]");
      await expect(banner).toContainText("This session’s folder no longer exists.");
      await expect(banner).toContainText(`${orphan.folder} was moved or deleted. You can read the history here, or continue in another folder.`);
      await expectPaused(page);

      // Polling keeps reporting the missing folder, which must not lift the pause.
      await page.waitForResponse(async (response) => new URL(response.url()).pathname === "/events" && (await response.json()).session_sync?.mode === "folder_missing");
      await expect(banner).toBeVisible();
      await expectPaused(page);
    });
  });
}

async function expectPaused(page) {
  const composer = page.getByLabel("Message to Pi");
  await expect(composer).toBeDisabled();
  await expect(composer).toHaveAttribute("placeholder", "Sending is paused.");
  await expect(page.locator(".send-button")).toBeDisabled();
}

async function openSidebar(page, touch) {
  if (touch && !await page.locator("#mobile-session-toggle").isChecked()) {
    await page.locator('label[aria-label="Open sessions"]').tap();
    // Row hit tests need the drawer to have finished sliding in.
    await expect(page.locator(".session-sidebar")).toHaveCSS("transform", "matrix(1, 0, 0, 1, 0, 0)");
  }
}
