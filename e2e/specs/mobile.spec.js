import { expect, test } from "@playwright/test";
import { mobileSubagents, nativeBash, prompts, replies, sessions, tool } from "../support/contract.mjs";
import { expectRunFinished, message, sendPrompt } from "../support/ui.mjs";

test("open and zoom live and persisted images on the first mobile tap", async ({ page }) => {
  await page.goto("/");
  await page.locator('label[aria-label="Open sessions"]').tap();
  const session = page.getByRole("link", { name: new RegExp(sessions.imageViewer) });
  if (!await session.isVisible()) await page.getByRole("link", { name: /Load \d+ more/ }).tap();
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
  await page.reload();

  const persistedImage = message(page, "user", prompt).getByRole("button", { name: "View attached image full size" });
  await expect(persistedImage).toBeVisible();
  await persistedImage.tap();
  await expect(viewer).toBeVisible();
  downloadPromise = page.waitForEvent("download");
  await viewer.getByRole("link", { name: "Download image" }).tap();
  download = await downloadPromise;
  expect(download.suggestedFilename()).toBe("image.png");
  await expect(viewer).toBeVisible();
  await viewer.locator("[data-image-viewer-stage]").tap({ position: { x: 10, y: 10 } });
  await expect(viewer).toBeHidden();
});

test("keep parallel subagent order and timestamps stable on mobile", async ({ page }) => {
  await page.goto("/");
  await page.locator('label[aria-label="Open sessions"]').tap();
  let session = page.getByRole("link", { name: new RegExp(sessions.parallelSubagentsMobile) });
  if (!await session.isVisible()) {
    await page.getByRole("button", { name: "Search sessions" }).tap();
    const search = page.getByRole("searchbox", { name: "Search sessions" });
    await search.fill(sessions.parallelSubagentsMobile);
    await search.press("Enter");
    session = page.getByRole("link", { name: new RegExp(sessions.parallelSubagentsMobile) });
  }
  await session.tap();
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

test("show a compact transcript toggle that switches on the first tap", async ({ page }) => {
  await page.goto("/");

  await page.locator('label[aria-label="Open sessions"]').tap();
  const history = page.getByRole("link", { name: new RegExp(sessions.history) });
  if (!await history.isVisible()) await page.getByRole("link", { name: /Load \d+ more/ }).tap();
  await history.tap();

  const toggle = page.getByRole("button", { name: "Messages-only transcript view" });
  await expect(toggle.getByText("All", { exact: true })).toBeVisible();
  const projectIcon = await page.locator(".session-header-project-icon").boundingBox();
  const viewIcon = await toggle.locator("[data-view-icon-frame]").boundingBox();
  const tapTarget = await toggle.boundingBox();
  expect(projectIcon).not.toBeNull();
  expect(viewIcon).not.toBeNull();
  expect(tapTarget).not.toBeNull();
  expect(viewIcon.width).toBe(projectIcon.width);
  expect(viewIcon.height).toBe(projectIcon.height);
  expect(tapTarget.width).toBeGreaterThanOrEqual(44);
  expect(tapTarget.height).toBeGreaterThanOrEqual(44);

  await toggle.tap();

  await expect(toggle).toHaveAttribute("aria-label", "Messages-only transcript view");
  await expect(toggle).toHaveAttribute("aria-pressed", "true");
  await expect(toggle).toHaveAttribute("title", "Show all details");
  await expect(toggle.getByText("Chat", { exact: true })).toBeVisible();
});

test("keep native Tab order for coarse pointers", async ({ page }) => {
  await page.goto("/");

  await page.locator('label[aria-label="Open sessions"]').click();
  const history = page.getByRole("link", { name: new RegExp(sessions.history) });
  if (!await history.isVisible()) await page.getByRole("link", { name: /Load \d+ more/ }).tap();
  await history.click();
  await expect(page.getByRole("heading", { level: 1, name: sessions.history })).toBeVisible();

  await page.locator("#conversation-scroll").focus();
  await page.keyboard.press("Tab");
  await expect(page.getByRole("button", { name: "Copy" })).toBeFocused();

  const composer = page.locator('textarea[name="message"]');
  await composer.focus();
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
  await page.goto("/");

  await page.locator('label[aria-label="Open sessions"]').click();
  await expect(page.getByRole("complementary", { name: "Sessions" })).toBeVisible();
  const mobileLink = page.getByRole("link", { name: new RegExp(sessions.mobile) });
  if (!await mobileLink.isVisible()) await page.getByRole("link", { name: /Load \d+ more/ }).tap();
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
  await page.goto("/");

  await page.locator('label[aria-label="Open sessions"]').click();
  const wrappedOutputLink = page.getByRole("link", { name: new RegExp(sessions.wrappedToolOutput) });
  if (!await wrappedOutputLink.isVisible()) await page.getByRole("link", { name: /Load \d+ more/ }).tap();
  await wrappedOutputLink.click();
  await expect(page.getByRole("heading", { level: 1, name: sessions.wrappedToolOutput })).toBeVisible();

  await sendPrompt(page, prompts.wrappedToolOutput);
  const card = page.locator(".message--tool-call").filter({ hasText: `$ ${tool.wrappedCommand}` }).last();
  await expectWrappedOutputCollapsed(card);

  await card.getByRole("button", { name: "Expand" }).tap();
  await expect(card.locator("[data-tool-output-toggle]")).toHaveAttribute("aria-expanded", "true");
  const region = card.getByRole("region", { name: "Expanded tool output" });
  await expect(region).toContainText("oldest-wrapped-output");
  await expect(region).toContainText("latest-wrapped-output");
  await expectRunFinished(page);

  await page.reload();
  await expectWrappedOutputCollapsed(page.locator(".message--tool-call").filter({ hasText: `$ ${tool.wrappedCommand}` }).last());
});

test("cancel a native bash command on the first mobile tap", async ({ page }) => {
  await page.goto("/");

  await page.locator('label[aria-label="Open sessions"]').click();
  const bashMobileLink = page.getByRole("link", { name: new RegExp(sessions.bashMobile) });
  if (!await bashMobileLink.isVisible()) await page.getByRole("link", { name: /Load \d+ more/ }).tap();
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
