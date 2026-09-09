import { expect, test as base } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { appendFile, readFile, unlink, writeFile } from "node:fs/promises";
import path from "node:path";
import { prompts, replies, sessions } from "../support/contract.mjs";
import { expectRunFinished, message, sendPrompt } from "../support/ui.mjs";

const externalTitle = "Active outside Gripi · notifications and unread indicators paused";
const test = base.extend({
  copiedSession: async ({ page }, use) => {
    await page.goto("/?show_all_sessions=1");
    const seedLink = page.getByRole("link", { name: new RegExp(sessions.marker) });
    const seedURL = new URL(await seedLink.getAttribute("href"), page.url());
    const seedPath = seedURL.searchParams.get("session");
    const entries = (await readFile(seedPath, "utf8")).trim().split("\n").map(JSON.parse);
    const id = randomUUID();
    const file = path.join(path.dirname(seedPath), `external-${id}.jsonl`);
    const title = `E2E External ${id.slice(0, 8)}`;
    entries[0].id = id;
    entries.find((entry) => entry.type === "session_info").name = title;
    await writeFile(file, `${entries.map((entry) => JSON.stringify(entry)).join("\n")}\n`);
    try {
      await use({ file, title, url: `/?${new URLSearchParams({ session: file, show_all_sessions: "1" })}`, backgroundURL: seedURL.href });
    } finally {
      await unlink(file);
    }
  },
});

for (const touch of [false, true]) {
  test.describe(touch ? "mobile touch" : "desktop", () => {
    // Keep the mobile regression self-contained despite the mobile project's restricted testMatch.
    test.use(touch ? { viewport: { width: 393, height: 851 }, isMobile: true, hasTouch: true } : {});

    test("external CLI activity stays quiet in live and reloaded sidebars until takeover", async ({ page, context, copiedSession }, testInfo) => {
      test.setTimeout(60_000);
      await context.addInitScript(() => {
        window.replyNotifications = [];
        document.hasFocus = () => false;
        localStorage.removeItem("gripi:notifications-disabled");
        window.gripiElectron = { showNotification: async (notification) => window.replyNotifications.push(notification) };
      });
      await page.goto(copiedSession.url);
      await expect(page.getByRole("heading", { level: 1, name: copiedSession.title })).toBeVisible();
      await expect(page.locator("#live-output")).toHaveAttribute("data-session-sync-mode", "available");

      await appendCLIReply(copiedSession.file, "First external CLI reply");
      await expect(message(page, "assistant", "First external CLI reply")).toBeVisible();
      await expect(page.locator("#live-output")).toHaveAttribute("data-session-sync-mode", "external_follow");
      await expect(page.getByLabel("Message to Pi")).toBeDisabled();
      await openSidebar(page, touch);
      const selectedLink = sessionLink(page, copiedSession.file);
      await expectExternalIcon(selectedLink);
      await page.screenshot({ path: testInfo.outputPath("external-session-icon.png") });

      const background = await context.newPage();
      await background.goto(copiedSession.backgroundURL);
      await openSidebar(background, touch);
      const backgroundLink = sessionLink(background, copiedSession.file);
      const sidebar = background.locator("#session-sidebar");
      await expectExternalIcon(backgroundLink);
      await expect(backgroundLink).not.toHaveClass(/\bunread\b/);
      const unreadCount = await sidebar.getAttribute("data-unread-session-count");
      const responseCount = Number(await backgroundLink.getAttribute("data-assistant-response-count"));

      await appendCLIReply(copiedSession.file, "Second external CLI reply");
      await expect(message(page, "assistant", "Second external CLI reply")).toBeAttached();
      // Wait for the real sidebar refresh to observe the appended JSONL, not just its old quiet row.
      await expect(backgroundLink).toHaveAttribute("data-assistant-response-count", String(responseCount + 1), { timeout: 15_000 });
      await expectExternalIcon(backgroundLink);
      await expect(backgroundLink).not.toHaveClass(/\bunread\b/);
      await expect(sidebar).toHaveAttribute("data-unread-session-count", unreadCount);
      expect(await background.evaluate(() => window.replyNotifications)).toEqual([]);
      expect(await page.evaluate(() => window.replyNotifications)).toEqual([]);

      // Keep the background sidebar external until the first gateway reply has completed.
      const sidebarURL = /\/sidebar(?:\?|$)/;
      await background.route(sidebarURL, (route) => route.abort());
      await background.reload();
      await openSidebar(background, touch);
      await expectExternalIcon(backgroundLink);
      await expect(backgroundLink).not.toHaveClass(/\bunread\b/);
      await expect(sidebar).toHaveAttribute("data-unread-session-count", unreadCount);
      await page.reload();
      await openSidebar(page, touch);
      await expectExternalIcon(selectedLink);
      if (touch) await page.locator('label[aria-label="Close sessions"]').tap();

      const takeover = page.getByRole("button", { name: "Take over in gateway", exact: true });
      if (touch) await takeover.tap();
      else await takeover.click();
      await expect(page.getByLabel("Message to Pi")).toBeEnabled();
      await expect(page.locator("#live-output")).toHaveAttribute("data-session-sync-mode", "managed");
      await expect(selectedLink.locator(".session-external-indicator")).toHaveCount(0);
      expect(await page.evaluate(() => window.replyNotifications)).toEqual([]);
      expect(await background.evaluate(() => window.replyNotifications)).toEqual([]);

      await sendPrompt(page, prompts.standard);
      await expect(message(page, "assistant", replies.standard)).toBeVisible();
      await expectRunFinished(page);
      await expectExternalIcon(backgroundLink);
      expect(await background.evaluate(() => window.replyNotifications)).toEqual([]);
      await background.unroute(sidebarURL);
      await expect(backgroundLink.locator(".session-external-indicator")).toHaveCount(0, { timeout: 15_000 });
      await expect(backgroundLink).toHaveAttribute("data-external-response-count", String(responseCount + 1));
      await expect(backgroundLink).toHaveAttribute("data-assistant-response-count", String(responseCount + 2));
      await expect.poll(() => page.evaluate(() => window.replyNotifications.map((notification) => notification.body)))
        .toEqual([replies.standard]);
      await expect.poll(() => background.evaluate(() => window.replyNotifications.map((notification) => notification.body)), { timeout: 15_000 })
        .toEqual([replies.standard]);
      await background.close();
    });
  });
}

function sessionLink(page, file) {
  return page.locator(`a.session[data-session-path="${file}"]`);
}

async function openSidebar(page, touch) {
  if (touch && !await page.locator("#mobile-session-toggle").isChecked()) {
    await page.locator('label[aria-label="Open sessions"]').tap();
  }
}

async function expectExternalIcon(link) {
  await expect(link).toHaveAttribute("data-session-sync-mode", "external_follow");
  const icon = link.locator("span.session-external-indicator");
  await expect(icon).toBeVisible();
  await expect(icon).toHaveAttribute("title", externalTitle);
  await expect(icon.locator("svg")).toBeVisible();
  await expect(icon).not.toHaveCSS("opacity", "0");
}

async function appendCLIReply(file, text) {
  const entries = (await readFile(file, "utf8")).trim().split("\n").map(JSON.parse);
  const previous = entries.at(-1);
  const timestamp = Math.max(Date.now(), Date.parse(previous.timestamp) + 1000);
  const assistant = entries.find((entry) => entry.message?.role === "assistant").message;
  const user = {
    type: "message", id: randomUUID().slice(0, 8), parentId: previous.id,
    timestamp: new Date(timestamp).toISOString(),
    message: { role: "user", content: [{ type: "text", text: `CLI request: ${text}` }], timestamp },
  };
  const reply = {
    type: "message", id: randomUUID().slice(0, 8), parentId: user.id,
    timestamp: new Date(timestamp + 1).toISOString(),
    message: { ...assistant, content: [{ type: "text", text }], timestamp: timestamp + 1 },
  };
  await appendFile(file, `${JSON.stringify(user)}\n${JSON.stringify(reply)}\n`);
}
