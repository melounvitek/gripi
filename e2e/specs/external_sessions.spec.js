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
    seedURL.searchParams.set("show_all_sessions", "1");
    const entries = (await readFile(seedPath, "utf8")).trim().split("\n").map(JSON.parse);
    const id = randomUUID();
    const file = path.join(path.dirname(seedPath), `external-${id}.jsonl`);
    const title = `E2E External ${id.slice(0, 8)} — frontend release hardening and installer readiness`;
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

    test("unopened CLI sessions stay compact and quiet, with first-tap navigation and actions", async ({ page, context, copiedSession }, testInfo) => {
      test.setTimeout(45_000);
      await context.addInitScript(() => {
        window.replyNotifications = [];
        document.hasFocus = () => false;
        localStorage.removeItem("gripi:notifications-disabled");
        window.gripiElectron = { showNotification: async (notification) => window.replyNotifications.push(notification) };
      });
      // Observe the CLI activity while a different conversation is selected.
      await page.goto(copiedSession.backgroundURL);
      await openSidebar(page, touch);
      const link = sessionLink(page, copiedSession.file);
      await expect(link).toBeVisible();
      const responseCount = Number(await link.getAttribute("data-assistant-response-count"));
      await appendCLIReply(copiedSession.file, "Unopened CLI reply");
      await expect(link).toHaveAttribute("data-assistant-response-count", String(responseCount + 1), { timeout: 15_000 });
      await expectExternalIcon(link);
      await expect(link).not.toHaveClass(/\bunread\b/);
      await expect(link).toHaveAttribute("data-external-response-count", String(responseCount + 1));
      await expectCompactRow(link, touch);
      expect(await page.evaluate(() => window.replyNotifications)).toEqual([]);
      await page.screenshot({ path: testInfo.outputPath("unopened-external-session.png") });

      await page.reload();
      await openSidebar(page, touch);
      await expectExternalIcon(link);
      await expect(link).not.toHaveClass(/\bunread\b/);
      await expectCompactRow(link, touch);
      expect(await page.evaluate(() => window.replyNotifications)).toEqual([]);

      const row = link.locator("..");
      const actions = row.locator("[data-session-actions-toggle]");
      const activate = (control) => touch ? control.tap() : control.click();
      for (const pinned of [true, false]) {
        await activate(actions);
        const pin = page.locator("[data-session-action-pin]");
        await expect(pin).toBeVisible();
        await expect(pin).toHaveText(pinned ? "Pin" : "Unpin");
        if (touch) expect((await pin.boundingBox()).height).toBeGreaterThanOrEqual(44);
        await activate(pin);
        await expect(row).toHaveAttribute("data-pinned", String(pinned));
        await expectCompactRow(link, touch);
      }

      await activate(actions);
      await activate(page.locator('[data-session-action="tags"]'));
      const tags = page.getByRole("dialog", { name: "Session tags", exact: true });
      const tag = `external-${touch ? "touch" : "desktop"}`;
      await tags.getByRole("searchbox", { name: "Find or create a tag" }).fill(tag);
      await activate(tags.getByRole("button", { name: `Create “${tag}”`, exact: true }));
      await expect(tags.getByRole("checkbox", { name: tag, exact: true })).toBeChecked();
      await activate(tags.getByRole("button", { name: "Close tag picker", exact: true }));
      await expect(row).toHaveAttribute("data-session-tags", JSON.stringify([tag]));
      await expectCompactRow(link, touch);

      await activate(link);
      await expect(page.getByRole("heading", { level: 1, name: copiedSession.title })).toBeVisible();
      if (touch) await expect(page.locator("#mobile-session-toggle")).not.toBeChecked();
      await openSidebar(page, touch);
      await expect(link).toHaveAttribute("aria-current", "page");
      await expectCompactRow(link, touch);
      await expect(row).toHaveCSS("background-color", "rgb(58, 58, 74)");
      await page.screenshot({ path: testInfo.outputPath("compact-selected-session.png") });
    });

    test("CLI updates leave controls, drafts, and the open sidebar intact", async ({ page, copiedSession }) => {
      await page.goto(copiedSession.url);
      const editor = page.getByLabel("Message to Pi");
      await editor.fill("Unsent draft");
      await page.locator("#image-input").setInputFiles("public/apple-touch-icon.png");
      await page.evaluate(() => {
        window.originalEditor = document.querySelector('.prompt-form textarea');
        window.originalMessage = document.querySelector('.message');
        window.blockingRefreshes = 0;
        new MutationObserver((records) => {
          if (records.some((record) => record.oldValue?.includes('session-switching')) || document.body.classList.contains('session-switching')) window.blockingRefreshes++;
        }).observe(document.body, { attributes: true, attributeFilter: ['class'], attributeOldValue: true });
      });
      const pending = await holdNextFragment(page);
      await appendCLIReply(copiedSession.file, "Nonblocking CLI reply");
      await pending.requested;
      await expect(page.locator("body")).not.toHaveClass(/session-switching/);
      await editor.fill("Draft edited while refreshing");
      await openSidebar(page, touch);
      pending.release();
      await expect(message(page, "assistant", "Nonblocking CLI reply")).toBeAttached();
      await expect(editor).toHaveValue("Draft edited while refreshing");
      await expect(editor).toBeDisabled();
      await expect(page.locator(".attachment-tray img")).toHaveCount(1);
      if (touch) await expect(page.locator("#mobile-session-toggle")).toBeChecked();
      expect(await page.evaluate(() => ({
        editor: window.originalEditor === document.querySelector('.prompt-form textarea'),
        message: window.originalMessage === document.querySelector('.message'),
        blockingRefreshes: window.blockingRefreshes,
      }))).toEqual({ editor: true, message: true, blockingRefreshes: 0 });

      await appendCLIReply(copiedSession.file, "Another nonblocking reply");
      await expect(message(page, "assistant", "Another nonblocking reply")).toBeAttached();
      if (touch) await expect(page.locator("#mobile-session-toggle")).toBeChecked();
      expect(await page.evaluate(() => window.blockingRefreshes)).toBe(0);
    });

    test("navigation wins over a pending CLI refresh on the first activation", async ({ page, copiedSession }) => {
      await page.goto(copiedSession.url);
      const pending = await holdNextFragment(page);
      await appendCLIReply(copiedSession.file, "Stale CLI reply");
      await pending.requested;
      await openSidebar(page, touch);
      const link = page.getByRole("link", { name: new RegExp(sessions.marker) });
      if (touch) await link.tap();
      else await link.click();
      await expect(page.getByRole("heading", { level: 1, name: sessions.marker })).toBeVisible();
      pending.release();
      await pending.finished;
      await expect(page.getByRole("heading", { level: 1, name: sessions.marker })).toBeVisible();
      await expect(message(page, "assistant", "Stale CLI reply")).toHaveCount(0);
    });

    test("failed CLI refresh keeps the page and recovers without navigation", async ({ page, copiedSession }) => {
      await page.goto(copiedSession.url);
      await page.evaluate(() => { window.retainedPage = true; });
      let fail = true;
      await page.route(/\/session_fragment(?:\?|$)/, (route) => fail ? route.fulfill({ status: 503, body: "Retry later" }) : route.continue());
      await appendCLIReply(copiedSession.file, "Recovered CLI reply");
      await expect(page.locator(".session-reconnect")).toHaveClass(/is-visible/);
      await expect(page.locator("body")).not.toHaveClass(/session-switching/);
      expect(await page.evaluate(() => window.retainedPage)).toBe(true);
      fail = false;
      await expect(message(page, "assistant", "Recovered CLI reply")).toBeVisible({ timeout: 15_000 });
      expect(await page.evaluate(() => window.retainedPage)).toBe(true);
    });

    test("CLI updates preserve loaded history and scrolling during the request", async ({ page, copiedSession }, testInfo) => {
      test.setTimeout(45_000);
      const originalEntries = (await readFile(copiedSession.file, "utf8")).trim().split("\n").map(JSON.parse);
      for (let index = 0; index < 90; index++) await appendCLIReply(copiedSession.file, `History reply ${index}`);
      await page.goto(copiedSession.url);
      await expect(message(page, "assistant", "History reply 89")).toBeVisible();
      const pending = await holdNextFragment(page);
      await appendCLIReply(copiedSession.file, "Update while reading history");
      await pending.requested;
      await page.locator("#conversation-scroll").evaluate((element) => {
        element.dispatchEvent(new WheelEvent('wheel', { deltaY: -1000 }));
        element.scrollTop = 0;
      });
      await expect(message(page, "assistant", "History reply 0")).toBeAttached();
      await expect(page.locator("#conversation-scroll")).toHaveAttribute("data-has-older-messages", "false");
      await page.locator("#conversation-scroll").evaluate((element) => { element.scrollTop = 120; });
      const anchor = message(page, "assistant", "History reply 0");
      const before = await anchor.evaluate((element) => {
        window.historyAnchor = element;
        return element.getBoundingClientRect().top;
      });
      pending.release();
      await expect(message(page, "assistant", "Update while reading history")).toBeAttached();
      await expect(anchor).toBeAttached();
      expect(await anchor.evaluate((element) => element === window.historyAnchor)).toBe(true);
      await expect.poll(() => anchor.evaluate((element) => element.getBoundingClientRect().top)).toBeCloseTo(before, 0);
      await page.screenshot({ path: testInfo.outputPath("cli-background-reading.png") });

      await page.locator("#conversation-scroll").evaluate((element) => {
        element.dispatchEvent(new WheelEvent('wheel', { deltaY: 1000 }));
        element.scrollTop = element.scrollHeight;
      });
      await appendCLIReply(copiedSession.file, "Follow the latest CLI reply");
      await expect(message(page, "assistant", "Follow the latest CLI reply")).toBeVisible();
      await expect.poll(() => page.locator("#conversation-scroll").evaluate((element) => element.scrollHeight - element.scrollTop - element.clientHeight)).toBeLessThan(5);

      // A native tree branch must replace the old branch, not append to it.
      await appendCLIReply(copiedSession.file, "Reply on another branch", originalEntries.at(-1).id);
      await expect(message(page, "assistant", "Reply on another branch")).toBeVisible();
      await expect(message(page, "assistant", "History reply 0")).toHaveCount(0);
      await expect(message(page, "assistant", "Follow the latest CLI reply")).toHaveCount(0);
      await page.reload();
      await expect(message(page, "assistant", "Reply on another branch")).toBeVisible();
      await expect(message(page, "assistant", "History reply 0")).toHaveCount(0);
    });

    test("CLI tool completion preserves already-loaded history when the paired article changes", async ({ page, copiedSession }) => {
      for (let index = 0; index < 90; index++) await appendCLIReply(copiedSession.file, `Tool history reply ${index}`);
      const entries = (await readFile(copiedSession.file, "utf8")).trim().split("\n").map(JSON.parse);
      const callID = randomUUID().slice(0, 8);
      const timestamp = Date.parse(entries.at(-1).timestamp) + 1000;
      await appendFile(copiedSession.file, JSON.stringify({
        type: "message", id: callID, parentId: entries.at(-1).id, timestamp: new Date(timestamp).toISOString(),
        message: { role: "assistant", content: [{ type: "toolCall", id: callID, name: "bash", arguments: { command: "printf paired-completion" } }] },
      }) + "\n");
      await page.goto(copiedSession.url);
      const history = message(page, "assistant", "Tool history reply 0");
      const conversation = page.locator("#conversation-scroll");
      await expect(history).toHaveCount(0);
      await conversation.evaluate((element) => { element.scrollTop = 0; });
      await expect(history).toBeAttached();
      await expect(conversation).toHaveAttribute("data-has-older-messages", "false");
      await history.evaluate((element) => { window.loadedToolHistory = element; });
      // Stay at the tail so a discarded prefix cannot silently reload via the history sentinel.
      await conversation.evaluate((element) => { element.scrollTop = element.scrollHeight; });
      const tool = page.locator(`article[data-tool-call-id="${callID}"]`);
      await expect(tool).toBeVisible();
      await expect(tool).not.toHaveAttribute("data-tool-result-persisted", "true");

      await appendFile(copiedSession.file, JSON.stringify({
        type: "message", id: randomUUID().slice(0, 8), parentId: callID, timestamp: new Date(timestamp + 1000).toISOString(),
        message: { role: "toolResult", toolCallId: callID, toolName: "bash", content: [{ type: "text", text: "Paired CLI tool completed" }], isError: false },
      }) + "\n");
      await expect(tool).toHaveCount(1);
      await expect(tool).toHaveAttribute("data-tool-result-persisted", "true");
      await expect(tool).toContainText("Paired CLI tool completed");
      await expect(history).toBeAttached();
      expect(await history.evaluate((element) => element === window.loadedToolHistory)).toBe(true);
      await expect(conversation).toHaveAttribute("data-has-older-messages", "false");
    });

    test("conversation find searches older history after CLI switches to a longer native branch", async ({ page, copiedSession }) => {
      const entries = (await readFile(copiedSession.file, "utf8")).trim().split("\n").map(JSON.parse);
      await appendCLIReply(copiedSession.file, "Older branch needle");
      for (let index = 0; index < 90; index++) await appendCLIReply(copiedSession.file, `Long branch reply ${index}`);
      const longBranch = (await readFile(copiedSession.file, "utf8")).trim().split("\n").map(JSON.parse).at(-1).id;
      await appendCLIReply(copiedSession.file, "Short branch reply", entries.at(-1).id);
      await page.goto(copiedSession.url);
      await expect(message(page, "assistant", "Short branch reply")).toBeVisible();
      await expect(page.locator("#conversation-scroll")).toHaveAttribute("data-has-older-messages", "false");
      await page.keyboard.press("Control+f");
      const find = page.getByRole("searchbox", { name: "Find in conversation" });
      const count = page.locator("[data-current-session-find-count]");
      await find.fill("Missing on short branch");
      await expect(count).toHaveText("0 / 0");

      await appendCLIReply(copiedSession.file, "Long branch resumed", longBranch);
      await expect(message(page, "assistant", "Long branch resumed")).toBeAttached();
      await expect(message(page, "assistant", "Short branch reply")).toHaveCount(0);
      await find.fill("Older branch needle");
      // The target occurs in both the CLI request and its assistant reply, outside the tail window.
      await expect(count).toHaveText("1 / 2");
      await expect(message(page, "assistant", "Older branch needle")).toBeAttached();
      await expect(page.locator("#conversation-scroll")).toHaveAttribute("data-has-older-messages", "false");
    });

    test("empty CLI snapshots clear previous extension status and widgets", async ({ page, copiedSession }) => {
      let state = {
        statuses: [{ statusKey: "cli", statusText: "CLI extension active" }],
        widgets: [{ widgetKey: "cli", widgetLines: ["CLI extension widget"], widgetPlacement: "aboveEditor" }],
      };
      await page.route(/\/session_fragment(?:\?|$)/, async (route) => {
        const payload = await (await route.fetch()).json();
        payload.conversation_html = payload.conversation_html.replace(/data-extension-ui-state="[^"]*"/,
          `data-extension-ui-state="${JSON.stringify(state).replaceAll('"', '&quot;')}"`);
        await route.fulfill({ json: payload });
      });
      await page.goto(copiedSession.url);
      await appendCLIReply(copiedSession.file, "CLI snapshot with extension state");
      const status = page.locator('[data-status-key="extension:cli"]');
      const widget = page.locator('[data-extension-widget-key="cli"]');
      await expect(status).toContainText("CLI extension active");
      await expect(widget).toContainText("CLI extension widget");

      state = {};
      await appendCLIReply(copiedSession.file, "CLI snapshot without extension state");
      await expect(message(page, "assistant", "CLI snapshot without extension state")).toBeAttached();
      await expect.soft(status).toHaveCount(0);
      await expect.soft(widget).toHaveCount(0);
    });

    test("CLI refresh preserves expanded output and conversation find", async ({ page, copiedSession }) => {
      const entries = (await readFile(copiedSession.file, "utf8")).trim().split("\n").map(JSON.parse);
      const callID = randomUUID().slice(0, 8);
      const timestamp = new Date().toISOString();
      await appendFile(copiedSession.file, [
        { type: "message", id: callID, parentId: entries.at(-1).id, timestamp,
          message: { role: "assistant", content: [{ type: "toolCall", id: callID, name: "example", arguments: { path: "output.txt" } }] } },
        { type: "message", id: randomUUID().slice(0, 8), parentId: callID, timestamp,
          message: { role: "toolResult", toolCallId: callID, toolName: "example", content: [{ type: "text", text: Array.from({ length: 80 }, (_, index) => `Output line ${index}`).join("\n") }], isError: false } },
      ].map(JSON.stringify).join("\n") + "\n");
      await page.goto(copiedSession.url);
      const expand = page.getByRole("button", { name: "Expand", exact: true });
      if (touch) await expand.tap();
      else await expand.click();
      await expect(page.locator("[data-tool-output-collapse]")).toHaveAttribute("data-expanded", "true");
      await page.keyboard.press("Control+f");
      const find = page.getByRole("searchbox", { name: "Find in conversation" });
      await find.fill("Output line 79");
      await appendCLIReply(copiedSession.file, "Reply while finding");
      await expect(message(page, "assistant", "Reply while finding")).toBeAttached();
      await expect(find).toHaveValue("Output line 79");
      await expect(find).toBeFocused();
      await expect(page.locator("[data-tool-output-collapse]")).toHaveAttribute("data-expanded", "true");
    });

    test("takeover wins over a pending CLI refresh", async ({ page, copiedSession }) => {
      await page.goto(copiedSession.url);
      await page.getByLabel("Message to Pi").fill("Draft retained through takeover");
      await appendCLIReply(copiedSession.file, "Before takeover");
      await expect(page.locator("#live-output")).toHaveAttribute("data-session-sync-mode", "external_follow");
      const pending = await holdNextFragment(page);
      await appendCLIReply(copiedSession.file, "Pending at takeover");
      await pending.requested;
      const takeover = page.getByRole("button", { name: "Take over in gateway", exact: true });
      if (touch) await takeover.tap();
      else await takeover.click();
      await expect(page.getByLabel("Message to Pi")).toBeEnabled();
      await expect(page.locator("#live-output")).toHaveAttribute("data-session-sync-mode", "managed");
      pending.release();
      await pending.finished;
      await expect(page.getByLabel("Message to Pi")).toBeEnabled();
      await expect(page.getByLabel("Message to Pi")).toHaveValue("Draft retained through takeover");
      await expect(message(page, "assistant", "Pending at takeover")).toBeAttached();
    });

    for (const retry of [false, true]) test(`takeover ${retry ? "retry" : "activation"} works on the first press when a CLI snapshot lands before release`, async ({ page, context, copiedSession }) => {
      await page.goto(copiedSession.url);
      await appendCLIReply(copiedSession.file, "Before takeover press");
      const takeover = page.getByRole("button", { name: "Take over in gateway", exact: true });
      await expect(takeover).toBeVisible();
      if (retry) {
        await page.route("/sessions/takeover", (route) => route.fulfill({ status: 503, json: { error: "Please retry takeover" } }), { times: 1 });
        if (touch) await takeover.tap();
        else await takeover.click();
        await expect(page.locator("[data-session-sync-error]")).toHaveText("Please retry takeover");
      }
      const pending = await holdNextFragment(page);
      await appendCLIReply(copiedSession.file, "Snapshot during takeover press");
      await pending.requested;
      if (touch) await takeover.tap({ trial: true });
      else await takeover.click({ trial: true });
      const box = await takeover.boundingBox();
      const point = { x: box.x + box.width / 2, y: box.y + box.height / 2 };
      const cdp = touch ? await context.newCDPSession(page) : null;
      if (touch) await cdp.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [point] });
      else {
        await page.mouse.move(point.x, point.y);
        await page.mouse.down();
      }
      pending.release();
      await expect(message(page, "assistant", "Snapshot during takeover press")).toBeAttached();
      await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
      if (touch) {
        await cdp.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
        await cdp.detach();
      } else await page.mouse.up();
      await expect(page.getByLabel("Message to Pi")).toBeEnabled();
      await expect(page.locator("#live-output")).toHaveAttribute("data-session-sync-mode", "managed");
    });

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
      await expectCompactRow(selectedLink, touch);
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
      await openSidebar(page, touch);
      await expect(selectedLink.locator("..").locator(".session-project")).toBeVisible();
      await expect(selectedLink.locator("..")).not.toHaveClass(/\bis-external\b/);
      if (touch) await page.locator('label[aria-label="Close sessions"]').tap();
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

async function expectCompactRow(link, touch) {
  const row = link.locator("..");
  await expect(row).toHaveClass(/\bis-external\b/);
  const title = link.locator(".session-title");
  await expect(title).toHaveCSS("white-space", "nowrap");
  await expect(title).toHaveCSS("text-overflow", "ellipsis");
  expect(await title.evaluate((element) => element.scrollWidth > element.clientWidth)).toBe(true);
  const actions = row.locator("[data-session-actions-toggle]");
  await expect(actions).toBeVisible();
  await expect(actions).toHaveCSS("opacity", "1");
  const age = row.locator(".session-meta");
  await expect(age).toHaveText(/^(now|\d+[mhd])$/);
  await expect(link).toHaveAttribute("title", / · .+ · \d{4}-\d{2}-\d{2} \d{2}:\d{2}$/);
  // Measure in one frame so the mobile drawer transition cannot skew relative positions.
  const [rowBox, linkBox, titleBox, ageBox, actionsBox] = await row.evaluate((element) =>
    [element, ...["a.session", ".session-title", ".session-meta", "[data-session-actions-toggle]"]
      .map((selector) => element.querySelector(selector))]
      .map((control) => control.getBoundingClientRect().toJSON()));
  expect(rowBox.height).toBeLessThanOrEqual(touch ? 46 : 36);
  expect(titleBox.x + titleBox.width).toBeLessThanOrEqual(ageBox.x);
  expect(ageBox.x + ageBox.width).toBeLessThanOrEqual(actionsBox.x);
  expect(actionsBox.x + actionsBox.width).toBeLessThanOrEqual(rowBox.x + rowBox.width);
  if (touch) {
    expect(linkBox.height).toBeGreaterThanOrEqual(44);
    expect(actionsBox.height).toBeGreaterThanOrEqual(44);
    expect(actionsBox.width).toBeGreaterThanOrEqual(44);
  }
}

async function holdNextFragment(page) {
  let requested, release, finished;
  const requestedPromise = new Promise((resolve) => { requested = resolve; });
  const released = new Promise((resolve) => { release = resolve; });
  const finishedPromise = new Promise((resolve) => { finished = resolve; });
  await page.route(/\/session_fragment(?:\?|$)/, async (route) => {
    const response = await route.fetch();
    requested();
    await released;
    await route.fulfill({ response }).catch(() => {});
    finished();
  }, { times: 1 });
  return { requested: requestedPromise, release, finished: finishedPromise };
}

async function appendCLIReply(file, text, parentId = null) {
  const entries = (await readFile(file, "utf8")).trim().split("\n").map(JSON.parse);
  const previous = entries.at(-1);
  const timestamp = Math.max(Date.now(), Date.parse(previous.timestamp) + 1000);
  const assistant = entries.find((entry) => entry.message?.role === "assistant").message;
  const user = {
    type: "message", id: randomUUID().slice(0, 8), parentId: parentId || previous.id,
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
