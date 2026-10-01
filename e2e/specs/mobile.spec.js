import { expect, test } from "@playwright/test";
import { mobileSubagents, nativeBash, prompts, replies, sessions, tool } from "../support/contract.mjs";
import { activityView, expectRunFinished, message, sendPrompt } from "../support/ui.mjs";
import {
  activateClearQueue, attachClearQueueDraft, expectClearedQueue, expectClearQueueDraft,
  expectClearQueueRunning, expectPendingQueue, prepareClearQueue, stopClearQueueRun
} from "../support/clear_queue.mjs";

test("read images stay visible with activity off and open on the first mobile tap", async ({ page }) => {
  await page.goto(`/?session_search=${encodeURIComponent(sessions.imageRead)}`);
  await page.locator('label[aria-label="Open sessions"]').tap();
  await page.getByRole("link", { name: new RegExp(sessions.imageRead) }).tap();
  const brief = activityView(page, "Brief");
  await brief.tap();
  await sendPrompt(page, prompts.imageRead);
  await expectRunFinished(page);

  for (const reload of [false, true]) {
    if (reload) {
      await page.reload();
      await brief.tap();
    }
    await expect(brief).toHaveAttribute("aria-pressed", "true");
    await expect(page.getByRole("region", { name: "Active now", exact: true })).toHaveCount(0);
    const image = page.getByRole("button", { name: "View attached image full size" }).last();
    await expect(image).toBeVisible();
    await image.tap();
    const viewer = page.getByRole("dialog", { name: "Full-size image viewer" });
    await expect(viewer).toBeVisible();
    await expect.poll(() => viewer.locator("img").evaluate((img) => img.naturalWidth)).toBeGreaterThan(0);
    await viewer.getByRole("button", { name: "Close image viewer" }).tap();
    await expect(viewer).toBeHidden();
  }
});

test("slash commands complete on the first mobile tap without submitting", async ({ page }) => {
  await page.goto(`/?${new URLSearchParams({ session_search: sessions.mobile })}`);
  await page.locator('label[aria-label="Open sessions"]').tap();
  await page.getByRole("link", { name: new RegExp(sessions.mobile) }).tap();
  await expect(page.getByRole("heading", { level: 1, name: sessions.mobile })).toBeVisible();
  const composer = page.getByLabel("Message to Pi");
  const modelCommand = page.locator('.command[data-command-name="model"]');
  await composer.fill("/mod");
  await expect(modelCommand).toBeVisible();
  await modelCommand.tap();
  await expect(composer).toHaveValue("/model ");
  await expect(page.getByRole("dialog", { name: "Model & thinking" })).toBeHidden();

  await composer.fill("/mod");
  await expect(modelCommand).toBeVisible();
  await composer.press("Enter");
  await expect(composer).toHaveValue("/model ");
  await expect(page.getByRole("dialog", { name: "Model & thinking" })).toBeHidden();
});

test("the model picker applies on the first mobile tap without raising the keyboard", async ({ page }) => {
  await page.goto(`/?${new URLSearchParams({ session_search: sessions.mobile })}`);
  await page.locator('label[aria-label="Open sessions"]').tap();
  await page.getByRole("link", { name: new RegExp(sessions.mobile) }).tap();
  const modelButton = page.getByRole("button", { name: "Open model and thinking settings" });
  await modelButton.tap();

  const dialog = page.getByRole("dialog", { name: "Model & thinking" });
  const search = dialog.getByRole("combobox", { name: "Search models" });
  await expect(dialog.getByRole("option")).toHaveCount(2);
  // A focused search field would open the keyboard over the list.
  await expect(search).not.toBeFocused();
  await dialog.getByRole("button", { name: "all" }).tap();
  await expect(dialog.getByRole("option")).toHaveCount(3);
  await expect(search).not.toBeFocused();
  const model = dialog.getByRole("option", { name: /contract-model/ });
  expect((await model.boundingBox()).height).toBeGreaterThanOrEqual(44);
  await model.tap();
  await expect(dialog).toBeHidden();
  await expect(modelButton).toContainText("e2e/contract-model (medium)");

  await modelButton.tap();
  // Closing and thinking levels are small labels, so they need a full 44px target in both directions.
  const level = dialog.getByRole("button", { name: "off", exact: true });
  for (const control of [level, dialog.getByRole("button", { name: "Close model and thinking settings" })]) {
    const bounds = await control.boundingBox();
    expect(bounds.width).toBeGreaterThanOrEqual(44);
    expect(bounds.height).toBeGreaterThanOrEqual(44);
  }
  await level.tap();
  await expect(dialog).toBeHidden();
  await expect(modelButton).toContainText("e2e/contract-model (off)");
});

test("attach images opens the picker on the first mobile tap from the right of the message field", async ({ page }) => {
  await page.goto(`/?${new URLSearchParams({ session_search: sessions.mobile })}`);
  await page.locator('label[aria-label="Open sessions"]').tap();
  await page.getByRole("link", { name: new RegExp(sessions.mobile) }).tap();
  await expect(page.getByRole("heading", { level: 1, name: sessions.mobile })).toBeVisible();
  const attach = page.locator('label[aria-label="Attach images"]');
  const bounds = await attach.boundingBox();
  const field = await page.getByLabel("Message to Pi").boundingBox();
  const send = await page.locator(".send-button").boundingBox();
  expect(bounds.width).toBeGreaterThanOrEqual(44);
  expect(bounds.height).toBeGreaterThanOrEqual(44);
  expect(bounds.x).toBeGreaterThanOrEqual(field.x + field.width - 1);
  expect(bounds.x + bounds.width).toBeLessThanOrEqual(send.x + 1);

  const chooserPromise = page.waitForEvent("filechooser", { timeout: 3000 });
  await attach.tap();
  const chooser = await chooserPromise;
  expect(chooser.isMultiple()).toBe(true);
  await chooser.setFiles({ name: "first-tap.png", mimeType: "image/png", buffer: await page.screenshot() });
  const attachment = page.locator(".attachment-tray .attachment");
  await expect(attachment).toContainText("first-tap.png");
  await attachment.getByRole("button", { name: "Remove" }).tap();
  await expect(attachment).toHaveCount(0);
});

test("Clear queue confirms on the first mobile tap with a 44px target", async ({ page }) => {
  try {
    await prepareClearQueue(page, sessions.clearQueueMobile, true);
    await attachClearQueueDraft(page);
    const clear = page.getByRole("button", { name: "Clear queue", exact: true });
    await expect(clear).toHaveText("×");
    await expect(clear).toHaveCSS("border-top-width", "0px");
    await expect(clear).toHaveCSS("background-color", "rgba(0, 0, 0, 0)");
    const bounds = await clear.boundingBox();
    expect(bounds).not.toBeNull();
    expect(bounds.width).toBe(44);
    expect(bounds.height).toBeGreaterThanOrEqual(44);
    expect(bounds.x).toBeGreaterThanOrEqual(0);
    expect(bounds.x + bounds.width).toBeLessThanOrEqual(page.viewportSize().width);
    const screenshot = test.info().outputPath("clear-queue-mobile.png");
    await page.screenshot({ path: screenshot });
    await test.info().attach("clear-queue-mobile", { path: screenshot, contentType: "image/png" });
    const requests = [];
    page.on("request", (request) => {
      if (new URL(request.url()).pathname === "/clear_queue") requests.push(request);
    });

    await activateClearQueue(page, { accept: false, mobile: true });
    await expectPendingQueue(page);
    await expectClearQueueDraft(page);
    await expectClearQueueRunning(page);
    expect(requests).toHaveLength(0);

    const responsePromise = page.waitForResponse("**/clear_queue");
    await activateClearQueue(page, { mobile: true });
    expect((await responsePromise).status()).toBe(200);
    expect(requests).toHaveLength(1);
    await expectClearedQueue(page);
    await expectClearQueueDraft(page);
  } finally {
    await stopClearQueueRun(page);
  }
});

test("open and zoom live and persisted images on the first mobile tap", async ({ page }) => {
  await page.goto("/?show_all_sessions=1");
  await page.locator('label[aria-label="Open sessions"]').tap();
  const session = page.getByRole("link", { name: new RegExp(sessions.imageViewer) });
  await session.tap();
  await expect(page.getByRole("heading", { level: 1, name: sessions.imageViewer })).toBeVisible();

  let releasePrompt;
  await page.route("**/prompt", async (route) => {
    await new Promise((resolve) => { releasePrompt = resolve; });
    await route.continue();
  });
  await page.locator("#image-input").setInputFiles({
    name: "mobile-image.png",
    mimeType: "image/png",
    buffer: await page.screenshot()
  });
  const prompt = "Inspect this mobile image";
  await sendPrompt(page, prompt);
  await expect.poll(() => typeof releasePrompt).toBe("function");

  const liveImage = message(page, "user", prompt).getByRole("button", { name: "View mobile-image.png full size" });
  await expect(liveImage).toBeVisible();
  await liveImage.tap();

  const viewer = page.getByRole("dialog", { name: "Full-size image viewer" });
  await expect(viewer).toBeVisible();
  await expect(viewer).not.toHaveAttribute("data-load-error", "true");
  const controls = viewer.locator("button, [data-image-viewer-download]");
  const sizes = await controls.evaluateAll((buttons) => buttons.map((button) => button.getBoundingClientRect()).map(({ width, height }) => ({ width, height })));
  expect(sizes.every(({ width, height }) => width >= 44 && height >= 44)).toBe(true);

  const zoomValue = viewer.locator("[data-image-viewer-zoom-value]");
  const fittedZoom = Number.parseInt(await zoomValue.textContent(), 10);
  await viewer.locator("[data-image-viewer-image]").dblclick();
  await expect(zoomValue).toHaveText("100%");
  await expect(viewer).toBeVisible();
  await viewer.getByRole("button", { name: "Fit image to screen" }).tap();
  await expect(zoomValue).toHaveText(`${fittedZoom}%`);
  await viewer.getByRole("button", { name: "Zoom in" }).tap();
  await expect.poll(async () => Number.parseInt(await zoomValue.textContent(), 10)).toBeGreaterThan(fittedZoom);
  await viewer.getByRole("button", { name: "Show actual size" }).tap();
  await expect(viewer.locator("[data-image-viewer-zoom-value]")).toHaveText("100%");

  let downloadPromise = page.waitForEvent("download");
  await viewer.getByRole("link", { name: "Download image" }).tap();
  let download = await downloadPromise;
  expect(download.suggestedFilename()).toBe("mobile-image.png");
  await expect(viewer).toBeVisible();

  releasePrompt();
  await expectRunFinished(page);
  await expect(viewer).toBeVisible();
  downloadPromise = page.waitForEvent("download");
  await viewer.getByRole("link", { name: "Download image" }).tap();
  download = await downloadPromise;
  expect(download.suggestedFilename()).toBe("mobile-image.png");
  await viewer.getByRole("button", { name: "Close image viewer" }).tap();
  await expect(viewer).toBeHidden();
  await page.setViewportSize({ width: 300, height: 700 });
  await page.reload();

  const persistedImage = message(page, "user", prompt).getByRole("button", { name: "View attached image full size" });
  await expect(persistedImage).toBeVisible();
  await persistedImage.tap();
  await expect(viewer).toBeVisible();
  const downloadBounds = await viewer.getByRole("link", { name: "Download image" }).boundingBox();
  expect(downloadBounds).not.toBeNull();
  expect(downloadBounds.x).toBeGreaterThanOrEqual(0);
  expect(downloadBounds.x + downloadBounds.width).toBeLessThanOrEqual(300);
  expect(downloadBounds.width).toBeGreaterThanOrEqual(44);
  expect(downloadBounds.height).toBeGreaterThanOrEqual(44);
  downloadPromise = page.waitForEvent("download");
  await viewer.getByRole("link", { name: "Download image" }).tap();
  download = await downloadPromise;
  expect(download.suggestedFilename()).toBe("image.png");
  await expect(viewer).toBeVisible();
  await viewer.locator("[data-image-viewer-stage]").tap({ position: { x: 10, y: 10 } });
  await expect(viewer).toBeHidden();
});

test("do not highlight unopened sessions on coarse pointers", async ({ page }) => {
  await page.goto("/");
  await page.locator('label[aria-label="Open sessions"]').tap();

  expect(await page.evaluate(() => matchMedia("(hover: hover) and (pointer: fine)").matches)).toBe(false);

  const session = page.locator('.session-row[data-current="false"] a.session').first();
  const restingStyle = await session.evaluate(sessionStyle);
  await session.hover();
  await session.evaluate((element) => Promise.all(element.getAnimations().map((animation) => animation.finished)));

  expect(await session.evaluate(sessionStyle)).toEqual(restingStyle);
  await expect(session).not.toHaveAttribute("aria-current", "page");
});

test("open selected session actions and pin from them on the first mobile tap", async ({ page }) => {
  await page.goto("/");
  await page.locator('label[aria-label="Open sessions"]').tap();

  const currentRow = page.locator('.session-row[data-current="true"]');
  const actions = currentRow.getByRole("button", { name: /Session actions/ });
  // Touch rows keep Pin in the actions menu, so the row stays one line.
  for (const pinned of [true, false]) {
    await actions.tap();
    await page.getByRole("menuitem", { name: pinned ? "Pin" : "Unpin", exact: true }).tap();
    await expect(currentRow).toHaveAttribute("data-pinned", String(pinned));
    await expect(page.getByRole("menu")).toBeHidden();
    await expect(actions).toHaveCSS("outline-style", "none");
  }

  const indicators = currentRow.locator(".session-indicators");
  await indicators.evaluate((element) => {
    const indicator = document.createElement("span");
    indicator.className = "session-fork-indicator";
    indicator.textContent = "⑂";
    element.append(indicator);
  });
  const indicatorBounds = await indicators.locator(".session-fork-indicator").boundingBox();
  const oneLineActionBounds = await actions.boundingBox();
  expect(indicatorBounds.x + indicatorBounds.width).toBeLessThanOrEqual(oneLineActionBounds.x);
  await indicators.locator(".session-fork-indicator").evaluate((element) => element.remove());

  const title = currentRow.locator(".session-title");
  await title.evaluate((element) => { element.textContent = "Fix sidebar session deletion and native rename behavior"; });
  const titleMetrics = await title.evaluate((element) => ({
    height: element.getBoundingClientRect().height,
    lineHeight: Number.parseFloat(getComputedStyle(element).lineHeight),
    truncated: element.scrollWidth > element.clientWidth,
  }));
  // Long titles stay on one line so each row keeps a compact, predictable height.
  expect(titleMetrics.height).toBeLessThanOrEqual(titleMetrics.lineHeight + 1);
  expect(titleMetrics.truncated).toBe(true);

  const bounds = await actions.boundingBox();
  const titleBounds = await title.boundingBox();
  expect(bounds).not.toBeNull();
  expect(titleBounds.x + titleBounds.width).toBeLessThanOrEqual(bounds.x);
  expect(Math.abs(titleBounds.y + titleBounds.height / 2 - bounds.y - bounds.height / 2)).toBeLessThan(1);
  expect(bounds.width).toBeGreaterThanOrEqual(44);
  expect(bounds.height).toBeGreaterThanOrEqual(44);

  await actions.tap();

  const menu = page.getByRole("menu");
  await expect(menu).toBeVisible();
  const menuBounds = await menu.boundingBox();
  expect(menuBounds.x).toBeGreaterThanOrEqual(0);
  expect(menuBounds.x + menuBounds.width).toBeLessThanOrEqual(page.viewportSize().width);
  await expect(page.getByRole("menuitem", { name: "Rename…" })).toBeVisible();
  await expect(page.getByRole("menuitem", { name: "Delete session…" })).toHaveAttribute("aria-disabled", "true");
});

test("keep parallel subagent order and timestamps stable on mobile", async ({ page }) => {
  await page.goto("/?show_all_sessions=1");
  await page.locator('label[aria-label="Open sessions"]').tap();
  await page.getByRole("link", { name: new RegExp(sessions.parallelSubagentsMobile) }).tap();
  await expect(page.getByRole("heading", { level: 1, name: sessions.parallelSubagentsMobile })).toBeVisible();
  await sendPrompt(page, prompts.parallelSubagentsMobile);

  const ids = [mobileSubagents.firstCallId, mobileSubagents.secondCallId];
  const cards = page.locator(ids.map((id) => `article[data-tool-call-id="${id}"]`).join(","));
  await expect(page.locator(`article[data-tool-call-id="${mobileSubagents.firstCallId}"]`)).toContainText(mobileSubagents.firstResult);
  await expect(page.locator(`article[data-tool-call-id="${mobileSubagents.secondCallId}"]`)).toContainText(mobileSubagents.secondProgress);
  const activeState = await cards.evaluateAll((entries) => entries.map((entry) => ({
    id: entry.dataset.toolCallId,
    timestamp: entry.dataset.messageTimestamp,
    label: entry.querySelector(".message-meta")?.textContent || ""
  })));
  expect(activeState.map(({ id }) => id)).toEqual(ids);
  expect(activeState.every(({ timestamp, label }) => timestamp && label)).toBe(true);

  await page.reload();
  await expect(page.getByRole("heading", { level: 1, name: sessions.parallelSubagentsMobile })).toBeVisible();
  await expect(cards).toHaveCount(2);
  expect(await cards.evaluateAll((entries) => entries.map((entry) => ({
    id: entry.dataset.toolCallId,
    timestamp: entry.dataset.messageTimestamp,
    label: entry.querySelector(".message-meta")?.textContent || ""
  })))).toEqual(activeState);

  await page.getByRole("button", { name: "Abort running Pi" }).tap();
  await expectRunFinished(page);
  await expect(cards).toHaveCount(2);
});

test("show agent activity segments that activate on the first tap", async ({ page }) => {
  await page.goto("/?show_all_sessions=1");

  await page.locator('label[aria-label="Open sessions"]').tap();
  await page.getByRole("link", { name: new RegExp(sessions.toolSummary) }).tap();
  await expect(page.getByRole("heading", { level: 1, name: sessions.toolSummary })).toBeVisible();
  const toolCalls = message(page, "assistant", `$ ${tool.longCommand}`);
  const previousCount = await toolCalls.count();
  await sendPrompt(page, prompts.longCommand);

  const toolCall = toolCalls.nth(previousCount);
  await expect(toolCall).toBeVisible();

  const brief = activityView(page, "Brief");
  const full = activityView(page, "Full");
  await expect(full).toHaveAttribute("aria-pressed", "true");
  for (const segment of [brief, full]) {
    const tapTarget = await segment.boundingBox();
    expect(tapTarget).not.toBeNull();
    expect(tapTarget.width).toBeGreaterThanOrEqual(44);
    expect(tapTarget.height).toBeGreaterThanOrEqual(44);
    expect(tapTarget.x).toBeGreaterThanOrEqual(0);
    expect(tapTarget.x + tapTarget.width).toBeLessThanOrEqual(page.viewportSize().width);
  }

  await brief.tap();

  await expect(brief).toHaveAttribute("aria-pressed", "true");
  await expect(toolCall).toBeHidden();
  await expect(message(page, "user", prompts.longCommand).last()).toBeVisible();

  await full.tap();

  await expect(full).toHaveAttribute("aria-pressed", "true");
  await expect(toolCall).toBeVisible();
  await expectRunFinished(page);
});

test("keep native Tab order for coarse pointers", async ({ page }) => {
  await page.goto(`/?${new URLSearchParams({ session_search: sessions.history })}`);

  await page.locator('label[aria-label="Open sessions"]').click();
  const history = page.getByRole("link", { name: new RegExp(sessions.history) });
  await history.click();
  await expect(page.getByRole("heading", { level: 1, name: sessions.history })).toBeVisible();

  await page.locator("#conversation-scroll").focus();
  await page.keyboard.press("Tab");
  await expect(page.getByRole("button", { name: "Copy" })).toBeFocused();

  const composer = page.locator('textarea[name="message"]');
  await composer.focus();
  await page.keyboard.press("Tab");
  await expect(page.locator("#image-input")).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(page.getByRole("button", { name: "Send" })).toBeFocused();
});

test("keep the mobile session drawer open while searching", async ({ page }) => {
  await page.goto("/");

  const drawerToggle = page.locator("#mobile-session-toggle");
  await page.locator('label[aria-label="Open sessions"]').tap();
  await page.getByRole("button", { name: "Search sessions" }).tap();
  const search = page.getByRole("searchbox", { name: "Search sessions" });
  await expect(search).toBeVisible();
  let releaseSidebar;
  await page.route("**/sidebar?**", async (route) => {
    if (new URL(route.request().url()).searchParams.get("session_search") !== "History Desktop") return route.continue();
    await new Promise((resolve) => { releaseSidebar = resolve; });
    await route.continue();
  });
  await search.fill("History Desktop");
  const submitted = page.waitForURL((url) => url.searchParams.get("session_search") === "History Desktop");
  await search.press("Enter");
  await expect.poll(() => !!releaseSidebar).toBe(true);
  await search.fill("different draft");
  releaseSidebar();
  await submitted;

  await expect(drawerToggle).toBeChecked();
  await expect(page.getByRole("searchbox", { name: "Search sessions" })).toHaveValue("History Desktop");
  await expect(page.getByRole("link", { name: new RegExp(sessions.history) })).toBeVisible();
});

test("navigate and complete a conversation from the mobile session drawer", async ({ page }) => {
  await page.goto("/?show_all_sessions=1");

  await page.locator('label[aria-label="Open sessions"]').click();
  await expect(page.getByRole("complementary", { name: "Sessions" })).toBeVisible();
  const mobileLink = page.getByRole("link", { name: new RegExp(sessions.mobile) });
  await mobileLink.click();
  await expect(page.getByRole("heading", { level: 1, name: sessions.mobile })).toBeVisible();
  await expect(page.locator("#mobile-session-toggle")).not.toBeChecked();

  const url = new URL("/notification-test", page.url()).href;
  const prompt = `Open ${url}.`;
  await sendPrompt(page, prompt);
  const userMessage = message(page, "user", prompt);
  const link = userMessage.getByRole("link", { name: url });
  await expect(link).toHaveAttribute("target", "_blank");
  await expect(link).toHaveAttribute("rel", "nofollow noreferrer noopener");
  const popupPromise = page.waitForEvent("popup");
  await link.tap();
  const popup = await popupPromise;
  await expect(popup).toHaveURL(url);
  await popup.close();
  await expect(message(page, "assistant", replies.standard)).toBeVisible();
  await expectRunFinished(page);

  await page.reload();
  await expect(page.getByRole("heading", { level: 1, name: sessions.mobile })).toBeVisible();
  await expect(message(page, "user", prompt).getByRole("link", { name: url })).toBeVisible();
  await expect(message(page, "assistant", replies.standard)).toBeVisible();
});

test("keep wrapped tool output short until the first Expand tap", async ({ page }) => {
  await page.goto("/?show_all_sessions=1");

  await page.locator('label[aria-label="Open sessions"]').click();
  await page.getByRole("link", { name: new RegExp(sessions.wrappedToolOutput) }).click();
  await expect(page.getByRole("heading", { level: 1, name: sessions.wrappedToolOutput })).toBeVisible();

  await sendPrompt(page, prompts.wrappedToolOutput);
  const card = page.locator(".message--tool-call").filter({ hasText: `$ ${tool.wrappedCommand}` }).last();
  await expectWrappedOutputCollapsed(card);

  // The quiet text toggle keeps a 44px touch target, and its edge still expands on the first tap.
  const expand = card.getByRole("button", { name: "Expand" });
  const target = await expand.evaluate((button) => {
    button.scrollIntoView({ block: "center" });
    const bounds = button.getBoundingClientRect();
    const x = bounds.left + bounds.width / 2;
    const y = bounds.top + bounds.height / 2;
    const hits = (dx, dy) => button.contains(document.elementFromPoint(x + dx, y + dy));
    return { height: bounds.height, width: bounds.width, reach: hits(0, -21) && hits(0, 21) && hits(-(bounds.width / 2 + 7), 0) && hits(bounds.width / 2 + 7, 0), border: getComputedStyle(button).borderTopWidth, transform: getComputedStyle(button).textTransform };
  });
  expect(target.reach).toBe(true);
  expect(target.border).toBe("0px");
  expect(target.transform).toBe("none");
  await expand.tap({ position: { x: target.width / 2, y: target.height / 2 + 19 } });
  await expect(card.locator("[data-tool-output-toggle]")).toHaveAttribute("aria-expanded", "true");
  const region = card.getByRole("region", { name: "Expanded tool output" });
  await expect(region).toContainText("oldest-wrapped-output");
  await expect(region).toContainText("latest-wrapped-output");
  await expectRunFinished(page);

  await page.reload();
  await expectWrappedOutputCollapsed(page.locator(".message--tool-call").filter({ hasText: `$ ${tool.wrappedCommand}` }).last());
});

test("cancel a native bash command on the first mobile tap", async ({ page }) => {
  await page.goto("/?show_all_sessions=1");

  await page.locator('label[aria-label="Open sessions"]').click();
  const bashMobileLink = page.getByRole("link", { name: new RegExp(sessions.bashMobile) });
  await bashMobileLink.click();
  await expect(page.getByRole("heading", { level: 1, name: sessions.bashMobile })).toBeVisible();

  await sendPrompt(page, `!${nativeBash.mobileCancel.command}`);
  const card = page.locator('article[data-role="bashExecution"]').filter({ hasText: `$ ${nativeBash.mobileCancel.command}` });
  await expect(card.getByRole("status", { name: "Shell command status" })).toContainText("running");

  await page.getByRole("button", { name: "Abort running Pi" }).tap();

  await expect(card).toHaveClass(/message--bash-cancelled/);
  await expect(card.getByRole("status", { name: "Shell command status" })).toContainText("cancelled");
  await expectRunFinished(page);
});

function sessionStyle(element) {
  const style = getComputedStyle(element);
  return { backgroundColor: style.backgroundColor, borderColor: style.borderColor };
}

async function expectWrappedOutputCollapsed(card) {
  const body = card.locator("[data-tool-output-body]");
  await expect(card.getByRole("button", { name: "Expand" })).toBeVisible();
  await expect(body).not.toContainText("oldest-wrapped-output");
  await expect(body).toContainText("latest-wrapped-output");

  const height = await body.evaluate((element) => element.getBoundingClientRect().height);
  expect(height).toBeLessThanOrEqual(300);
  expect(await textIsVisible(body, "latest-wrapped-output")).toBe(true);
}

async function textIsVisible(container, text) {
  return container.evaluate((element, expectedText) => {
    const node = [...element.querySelectorAll(".tool-output-line")]
      .map((line) => line.firstChild)
      .find((candidate) => candidate?.textContent.includes(expectedText));
    if (!node) return false;

    const start = node.textContent.indexOf(expectedText);
    const range = document.createRange();
    range.setStart(node, start);
    range.setEnd(node, start + expectedText.length);
    const containerRect = element.getBoundingClientRect();
    const textRect = range.getBoundingClientRect();
    return textRect.top >= containerRect.top && textRect.bottom <= containerRect.bottom;
  }, text);
}
