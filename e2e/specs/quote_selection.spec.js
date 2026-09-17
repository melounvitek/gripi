import { expect, test } from "@playwright/test";
import { prompts, replies, sessions } from "../support/contract.mjs";
import { expectRunFinished, message, selectSession, sendPrompt } from "../support/ui.mjs";

async function selectText(body) {
  await body.scrollIntoViewIfNeeded();
  return body.evaluate(async (element) => {
    // Let pending scroll events dismiss any old Quote before creating the selection.
    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    const range = document.createRange();
    range.selectNodeContents(element);
    const selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
    return selection.toString();
  });
}

async function openQuoteSession(page, title) {
  await page.goto(`/?${new URLSearchParams({ session_search: title })}`);
  await selectSession(page, title);
}

test("quotes history without replacing drafts or attachments, sending, or losing the saved draft", async ({ page }) => {
  await openQuoteSession(page, sessions.quoteHistory);
  const composer = page.getByLabel("Message to Pi");
  await composer.fill("My existing question  ");
  await page.locator("#image-input").setInputFiles({ name: "quote.png", mimeType: "image/png", buffer: await page.screenshot() });
  const attachment = page.locator(".attachment-tray");
  await expect(attachment).toContainText("quote.png");
  const requests = [];
  page.on("request", (request) => {
    if (["/prompt", "/bash"].includes(new URL(request.url()).pathname)) requests.push(request);
  });
  const body = message(page, "assistant", `Fixture answer for ${sessions.quoteHistory}`).locator(".message-body");
  const text = await selectText(body);
  const quote = page.getByRole("button", { name: "Quote selection", exact: true });
  await expect(quote).toBeVisible();
  expect(await page.evaluate(() => getSelection().toString())).toBe(text);
  await quote.click();
  const expected = `My existing question  \n\n> ${text}\n\n`;
  await expect(composer).toHaveValue(expected);
  await expect(composer).toBeFocused();
  expect(await composer.evaluate((el) => [el.selectionStart, el.selectionEnd])).toEqual([expected.length, expected.length]);
  await expect(attachment).toContainText("quote.png");
  await expect(quote).toBeHidden();
  expect(requests).toHaveLength(0);
  await page.reload();
  await expect(composer).toHaveValue(expected);
});

test("preserves native Ctrl+C copying without changing the draft", async ({ page, context }) => {
  await openQuoteSession(page, sessions.quoteHistory);
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.evaluate(() => navigator.clipboard.writeText(""));
  const composer = page.getByLabel("Message to Pi");
  await composer.fill("Keep this draft while copying");
  await page.locator("#conversation-scroll").focus();
  const body = message(page, "assistant", `Fixture answer for ${sessions.quoteHistory}`).locator(".message-body");
  const text = await selectText(body);
  await expect(page.getByRole("button", { name: "Quote selection", exact: true })).toBeVisible();
  await page.keyboard.press("Control+c");
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(text);
  expect(await page.evaluate(() => getSelection().toString())).toBe(text);
  await expect(composer).toHaveValue("Keep this draft while copying");
});

test("quotes live messages and their reloaded history, with keyboard activation", async ({ page }) => {
  await openQuoteSession(page, sessions.quoteLive);
  const responses = message(page, "assistant", replies.standard);
  const previousCount = await responses.count();
  await sendPrompt(page, prompts.standard);
  await expectRunFinished(page);
  await expect(responses).toHaveCount(previousCount + 1);
  const body = responses.last().locator(".message-body");
  const quote = page.getByRole("button", { name: "Quote selection", exact: true });
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload();
      await expect(page.getByLabel("Message to Pi")).toHaveValue(`> ${replies.standard}\n\n`);
    }
    await page.getByLabel("Message to Pi").fill("");
    await selectText(body);
    await expect(quote).toBeVisible();
    await page.keyboard.press("Tab");
    await expect(quote).toBeFocused();
    await page.keyboard.press("Enter");
    await expect(page.getByLabel("Message to Pi")).toHaveValue(`> ${replies.standard}\n\n`);
  }
});

test("quotes multiline code without Copy labels", async ({ page }) => {
  await openQuoteSession(page, sessions.quoteLive);
  const responses = message(page, "assistant", "Modal fence stays in conversation");
  const previousCount = await responses.count();
  await sendPrompt(page, prompts.markdownFence);
  await expectRunFinished(page);
  await expect(responses).toHaveCount(previousCount + 1);
  const body = responses.last().locator(".message-body");
  const text = await selectText(body);
  const quote = page.getByRole("button", { name: "Quote selection", exact: true });
  await expect(quote).toBeVisible();
  await quote.click();
  expect(text).not.toContain("Copy");
  expect(text).toContain("const safeValue = 42;");
  await expect(page.getByLabel("Message to Pi")).toHaveValue(text.split("\n").map((line) => `> ${line}`).join("\n") + "\n\n");
});

for (const layout of ["short", "multiline", "viewport-filling"]) {
  test(`keeps the compact desktop Quote outside a ${layout} selection`, async ({ page }, testInfo) => {
    await openQuoteSession(page, sessions.quoteHistory);
    const body = page.locator('article[data-role="assistant"] .message-body').last();
    if (layout !== "short") {
      await body.evaluate((element, lines) => {
        element.style.whiteSpace = "pre-wrap";
        element.textContent = Array.from({ length: lines }, (_, i) => `Selected line ${i + 1}: leave this text readable.`).join("\n");
      }, layout === "multiline" ? 4 : 60);
    }
    await selectText(body);
    const quote = page.getByRole("button", { name: "Quote selection", exact: true });
    await expect(quote).toBeVisible();
    await expect(quote).toHaveCSS("text-transform", "none");
    await expect(quote).toHaveCSS("height", "30px");
    expect(await quote.innerText()).toBe("Quote");
    const bounds = await quote.boundingBox();
    const selected = await page.evaluate(() => {
      const rect = getSelection().getRangeAt(0).getBoundingClientRect();
      const scroll = document.querySelector("#conversation-scroll").getBoundingClientRect();
      return { left: Math.max(rect.left, scroll.left), right: Math.min(rect.right, scroll.right), top: Math.max(rect.top, scroll.top), bottom: Math.min(rect.bottom, scroll.bottom) };
    });
    expect(bounds.width).toBeLessThan(100);
    expect(bounds.x).toBeGreaterThanOrEqual(0);
    expect(bounds.x + bounds.width).toBeLessThanOrEqual(page.viewportSize().width);
    expect(bounds.y).toBeGreaterThanOrEqual(0);
    expect(bounds.y + bounds.height).toBeLessThanOrEqual((await page.locator(".composer").boundingBox()).y);
    const overlaps = bounds.x < selected.right && bounds.x + bounds.width > selected.left && bounds.y < selected.bottom && bounds.y + bounds.height > selected.top;
    expect(overlaps).toBe(false);
    if (layout === "short") {
      expect(bounds.y).toBeGreaterThanOrEqual(selected.bottom);
      const lastRectRight = await page.evaluate(() => [...getSelection().getRangeAt(0).getClientRects()].filter((rect) => rect.width && rect.height).at(-1).right);
      expect(bounds.x + bounds.width).toBeCloseTo(lastRectRight, 0);
    }
    for (const hover of [false, true]) {
      if (hover) await quote.hover();
      const rgb = await quote.evaluate((element) => getComputedStyle(element).backgroundColor.match(/\d+/g).slice(0, 3).map(Number));
      expect(Math.max(...rgb)).toBeLessThan(90);
    }
    await page.screenshot({ path: testInfo.outputPath(`quote-${layout}.png`) });
    await quote.click();
    await expect(page.getByLabel("Message to Pi")).toHaveValue(/^> /);
  });
}

test("quotes a mouse-drag selection", async ({ page }) => {
  await openQuoteSession(page, sessions.quoteHistory);
  const body = message(page, "assistant", `Fixture answer for ${sessions.quoteHistory}`).locator(".message-body");
  await body.scrollIntoViewIfNeeded();
  const bounds = await body.evaluate((element) => {
    const text = element.querySelector("p").firstChild;
    const range = document.createRange();
    range.setStart(text, 0);
    range.setEnd(text, "Fixture answer".length);
    const rect = range.getBoundingClientRect();
    return { x: rect.x, y: rect.y + rect.height / 2, right: rect.right };
  });
  await page.mouse.move(bounds.x, bounds.y);
  await page.mouse.down();
  await page.mouse.move(bounds.right, bounds.y, { steps: 5 });
  await page.mouse.up();
  expect(await page.evaluate(() => getSelection().toString())).toBe("Fixture answer");
  await page.getByRole("button", { name: "Quote selection", exact: true }).click();
  await expect(page.getByLabel("Message to Pi")).toHaveValue("> Fixture answer\n\n");
});

test("dismisses a selected streaming passage when its source is replaced", async ({ page }) => {
  await openQuoteSession(page, sessions.quoteLive);
  await sendPrompt(page, prompts.deltaStreaming);
  const body = message(page, "assistant", replies.deltaTextStart).last().locator(".message-body");
  await expect(body).toHaveText(replies.deltaTextStart);
  await selectText(body);
  const quote = page.getByRole("button", { name: "Quote selection", exact: true });
  await expect(quote).toBeVisible();
  await expectRunFinished(page);
  await expect(quote).toBeHidden();
  await expect(page.getByLabel("Message to Pi")).toHaveValue("");
  await expect(body).toHaveText(replies.deltaText);
  await selectText(body);
  await quote.click();
  await expect(page.getByLabel("Message to Pi")).toHaveValue(`> ${replies.deltaText}\n\n`);
});

test("cleans up selections across session switches and does not duplicate handlers", async ({ page }) => {
  await openQuoteSession(page, sessions.quoteHistory);
  const url = new URL(page.url());
  url.searchParams.delete("session_search");
  url.searchParams.set("sidebar_sessions_limit", "100");
  await page.goto(url.toString());
  await page.evaluate(() => { window.quoteNavigationSentinel = true; });
  const composer = page.getByLabel("Message to Pi");
  await composer.fill("Saved draft");
  const body = message(page, "assistant", `Fixture answer for ${sessions.quoteHistory}`).locator(".message-body");
  const quote = page.getByRole("button", { name: "Quote selection", exact: true });
  await selectText(body);
  await expect(quote).toBeVisible();
  await selectSession(page, sessions.history);
  await expect(quote).toBeHidden();
  await expect(composer).toHaveValue("");
  await selectSession(page, sessions.quoteHistory);
  await expect(composer).toHaveValue("Saved draft");
  await expect(quote).toBeHidden();
  const text = await selectText(body);
  await quote.click();
  await expect(composer).toHaveValue(`Saved draft\n\n> ${text}\n\n`);
  expect(await page.evaluate(() => window.quoteNavigationSentinel)).toBe(true);
});

test("does not insert into a composer that becomes disabled", async ({ page }) => {
  await openQuoteSession(page, sessions.quoteHistory);
  const composer = page.getByLabel("Message to Pi");
  await composer.fill("Keep this");
  const body = message(page, "assistant", `Fixture answer for ${sessions.quoteHistory}`).locator(".message-body");
  const quote = page.getByRole("button", { name: "Quote selection", exact: true });
  await selectText(body);
  await expect(quote).toBeVisible();
  await composer.evaluate((element) => { element.disabled = true; });
  await quote.click();
  await expect(composer).toHaveValue("Keep this");
  await selectText(body);
  await expect(quote).toBeHidden();
});

test("dismisses invalid selections and Escape without changing the draft", async ({ page }) => {
  await openQuoteSession(page, sessions.quoteHistory);
  const composer = page.getByLabel("Message to Pi");
  await composer.fill("Keep this");
  const body = message(page, "assistant", `Fixture answer for ${sessions.quoteHistory}`).locator(".message-body");
  const quote = page.getByRole("button", { name: "Quote selection", exact: true });
  await selectText(body);
  await expect(quote).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(quote).toBeHidden();
  await selectText(body);
  await expect(quote).toBeVisible();
  await page.evaluate(() => getSelection().removeAllRanges());
  await expect(quote).toBeHidden();
  await selectText(body.locator(".."));
  await expect(quote).toBeHidden();

  // Both endpoints are valid message text, but the range spans two messages.
  await selectText(body);
  await expect(quote).toBeVisible();
  const selected = await body.evaluate((element) => {
    const userText = document.querySelector('article[data-role="user"] .message-body').firstChild;
    const assistantText = element.querySelector("p").firstChild;
    const range = document.createRange();
    range.setStart(userText, 0);
    range.setEnd(assistantText, assistantText.length);
    const selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
    return selection.toString();
  });
  expect(selected).toContain(`Fixture question for ${sessions.quoteHistory}`);
  expect(selected).toContain(`Fixture answer for ${sessions.quoteHistory}`);
  await expect(quote).toBeHidden();
  await expect(composer).toHaveValue("Keep this");
});
