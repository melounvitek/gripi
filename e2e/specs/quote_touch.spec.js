import { expect, test } from "@playwright/test";
import { prompts, replies, sessions } from "../support/contract.mjs";
import { expectRunFinished, message, sendPrompt } from "../support/ui.mjs";

const titles = {
  mobile: { persisted: sessions.quoteMobileHistory, live: sessions.quoteMobileLive },
  iphone: { persisted: sessions.quoteIphoneHistory, live: sessions.quoteIphoneLive }
};

// These tests use real DOM Selection/Range with simulated takeover/handle/viewport
// events. They do not cover native iOS long-press or Add-to-Home-Screen behavior.
test.describe("touch Quote selection", () => {
  for (const rendering of ["persisted", "live"]) {
    test(`quote ${rendering} text exactly once on the first tap and allow another selection`, async ({ page }, testInfo) => {
      const title = titles[testInfo.project.name][rendering];
      await openQuoteSession(page, title);
      if (rendering === "live") {
        const responses = message(page, "assistant", replies.standard);
        const previousCount = await responses.count();
        await sendPrompt(page, prompts.standard);
        await expectRunFinished(page);
        await expect(responses).toHaveCount(previousCount + 1);
      }
      const text = rendering === "live" ? replies.standard : `Fixture answer for ${title}`;
      const body = message(page, "assistant", text).last().locator(".message-body");
      const selectedText = rendering === "live" ? "browser response" : "Fixture answer";
      const secondText = rendering === "live" ? "complete." : title;
      const composer = page.getByLabel("Message to Pi");
      const draft = "Keep this mobile draft  ";
      await composer.fill(draft);
      await page.locator("#image-input").setInputFiles({ name: "touch-quote.png", mimeType: "image/png", buffer: await page.screenshot() });
      const attachment = page.locator(".attachment-tray");
      await expect(attachment).toContainText("touch-quote.png");
      const userMessages = page.locator('article[data-role="user"]');
      const initialCount = await userMessages.count();
      const requests = recordSendRequests(page);

      await selectMobileMessageText(body, selectedText);
      const quote = page.getByRole("button", { name: "Quote selection", exact: true });
      await expect(quote).toBeVisible();
      await expect(quote).toHaveAttribute("data-quote-selection");
      expect(await page.evaluate(() => window.getSelection().toString())).toBe(selectedText);
      await expect(composer).toHaveValue(draft);
      await expectTouchQuoteBounds(page, quote);

      // Simulate successive selection-handle adjustments, without lifting a DOM pointer.
      await selectMobileMessageText(body, secondText);
      await selectMobileMessageText(body, text);
      await expect(quote).toBeVisible();
      expect(await page.evaluate(() => window.getSelection().toString())).toBe(text);
      await quote.tap();
      const firstDraft = `${draft}\n\n> ${text}\n\n`;
      await expect(composer).toHaveValue(firstDraft);
      await expect(composer).toBeFocused();
      await expect(attachment).toContainText("touch-quote.png");
      await expect(userMessages).toHaveCount(initialCount);
      expect(requests).toHaveLength(0);

      await selectMobileMessageText(body, secondText);
      await expect(quote).toBeVisible();
      expect(await page.evaluate(() => window.getSelection().toString())).toBe(secondText);
      await quote.tap();
      await expect(composer).toHaveValue(`${firstDraft}> ${secondText}\n\n`);
      await expect(composer).toBeFocused();
      await expect(attachment).toContainText("touch-quote.png");
      await expect(userMessages).toHaveCount(initialCount);
      expect(requests).toHaveLength(0);
    });
  }

  for (const activation of ["pointer", "keyboard"]) {
    test(`use the latest selection when ${activation} activation beats the next animation frame`, async ({ page }, testInfo) => {
      const title = titles[testInfo.project.name].persisted;
      await openQuoteSession(page, title);
      const composer = page.getByLabel("Message to Pi");
      await composer.fill("Keep this draft");
      const body = message(page, "assistant", `Fixture answer for ${title}`).locator(".message-body");
      await selectMobileMessageText(body, "Fixture answer");
      const quote = page.getByRole("button", { name: "Quote selection", exact: true });
      await expect(quote).toBeVisible();
      await quote.evaluate((button, method) => {
        const selection = getSelection();
        const range = selection.getRangeAt(0).cloneRange();
        range.setStart(range.startContainer, range.startOffset + "Fixture ".length);
        selection.removeAllRanges();
        selection.addRange(range);
        // Force the ordering: a changed selection is pending when activation begins.
        document.dispatchEvent(new Event("selectionchange"));
        if (method === "pointer") button.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, pointerType: "touch" }));
        else document.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Tab" }));
        selection.removeAllRanges();
        document.dispatchEvent(new Event("selectionchange"));
        button.click();
      }, activation);
      await expect(composer).toHaveValue("Keep this draft\n\n> answer\n\n");
    });
  }

  for (const event of ["message pointercancel", "visualViewport resize", "visualViewport scroll", "conversation scroll"]) {
    test(`retain a valid touch selection after simulated ${event}`, async ({ page }, testInfo) => {
      const title = titles[testInfo.project.name].persisted;
      await openQuoteSession(page, title);
      const body = message(page, "assistant", `Fixture answer for ${title}`).locator(".message-body");
      const composer = page.getByLabel("Message to Pi");
      await composer.fill("Keep this takeover draft");
      const requests = recordSendRequests(page);
      if (event === "conversation scroll") {
        // Make this short history genuinely scrollable without changing the selected source.
        await body.evaluate((element) => {
          const article = element.closest("article");
          article.style.marginTop = "300px";
          article.style.marginBottom = "600px";
        });
      }
      if (event === "message pointercancel") await touchPointer(body, "pointerdown");
      await selectMobileMessageText(body, "Fixture answer");
      const quote = page.getByRole("button", { name: "Quote selection", exact: true });
      await expect(quote).toBeVisible();
      const before = await quote.boundingBox();
      if (event === "message pointercancel") {
        // Browser takes over the gesture after establishing a non-collapsed selection;
        // there is deliberately no pointerup to restore the Quote action.
        await touchPointer(body, "pointercancel");
      } else if (event.startsWith("visualViewport")) {
        await page.evaluate((type) => {
          const viewport = window.visualViewport;
          if (type === "resize") Object.defineProperty(viewport, "height", { configurable: true, value: 420 });
          else {
            Object.defineProperty(viewport, "offsetTop", { configurable: true, value: 24 });
            Object.defineProperty(viewport, "height", { configurable: true, value: 676 });
          }
          viewport.dispatchEvent(new Event(type));
        }, event.split(" ")[1]);
      } else {
        const scroller = page.locator("#conversation-scroll");
        const scrollTop = await scroller.evaluate((element) => element.scrollTop);
        await scroller.evaluate((element) => { element.scrollTop += 24; });
        await expect.poll(() => scroller.evaluate((element) => element.scrollTop)).toBe(scrollTop + 24);
      }
      // Flush queued scroll/selection events so visibility cannot pass before dismissal.
      await settleSelection(page);
      expect(await page.evaluate(() => window.getSelection().toString())).toBe("Fixture answer");
      await expect(quote).toBeVisible();
      await expectTouchQuoteBounds(page, quote);
      if (event === "visualViewport resize") expect((await quote.boundingBox()).y).toBeLessThan(before.y);
      await quote.tap();
      await expect(composer).toHaveValue("Keep this takeover draft\n\n> Fixture answer\n\n");
      expect(requests).toHaveLength(0);
    });
  }

  for (const input of ["simulated pointer", "Chromium touchCancel"]) {
    test(`cancel a Quote selection touch without inserting or sending (${input})`, async ({ page, context, browserName }, testInfo) => {
      test.skip(input === "Chromium touchCancel" && browserName !== "chromium", "CDP input is Chromium-only");
      const title = titles[testInfo.project.name].persisted;
      await openQuoteSession(page, title);
      const body = message(page, "assistant", `Fixture answer for ${title}`).locator(".message-body");
      const composer = page.getByLabel("Message to Pi");
      const draft = "Keep this draft after cancellation";
      await composer.fill(draft);
      const userMessages = page.locator('article[data-role="user"]');
      const initialCount = await userMessages.count();
      const requests = recordSendRequests(page);
      await selectMobileMessageText(body, "Fixture answer");
      const quote = page.getByRole("button", { name: "Quote selection", exact: true });
      await expect(quote).toBeVisible();
      if (input === "simulated pointer") {
        await touchPointer(quote, "pointerdown");
        await touchPointer(quote, "pointercancel");
      } else {
        const bounds = await quote.boundingBox();
        expect(bounds).not.toBeNull();
        // Playwright has no touch-cancel helper; retain actual Chromium input coverage.
        const touch = await context.newCDPSession(page);
        try {
          await touch.send("Input.dispatchTouchEvent", {
            type: "touchStart",
            touchPoints: [{ x: bounds.x + bounds.width / 2, y: bounds.y + bounds.height / 2 }]
          });
          await touch.send("Input.dispatchTouchEvent", { type: "touchCancel", touchPoints: [] });
        } finally {
          await touch.detach();
        }
      }
      await settleSelection(page);
      await expect(composer).toHaveValue(draft);
      await expect(userMessages).toHaveCount(initialCount);
      expect(requests).toHaveLength(0);

      await selectMobileMessageText(body, title);
      await expect(quote).toBeVisible();
      await quote.tap();
      await expect(composer).toHaveValue(`${draft}\n\n> ${title}\n\n`);
      await expect(composer).toBeFocused();
      await expect(userMessages).toHaveCount(initialCount);
      expect(requests).toHaveLength(0);
    });
  }

  for (const invalidation of ["cleared selection", "replaced source"]) {
    test(`invalidate a touch quote after ${invalidation}`, async ({ page }, testInfo) => {
      const title = titles[testInfo.project.name].persisted;
      await openQuoteSession(page, title);
      const body = message(page, "assistant", `Fixture answer for ${title}`).locator(".message-body");
      const composer = page.getByLabel("Message to Pi");
      await composer.fill("Keep this invalidated draft");
      const requests = recordSendRequests(page);
      await selectMobileMessageText(body, "Fixture answer");
      const quote = page.getByRole("button", { name: "Quote selection", exact: true });
      await expect(quote).toBeVisible();
      if (invalidation === "cleared selection") await page.evaluate(() => window.getSelection().removeAllRanges());
      else await body.evaluate((element) => { element.innerHTML = "<p>Replacement streamed passage</p>"; });
      await expect(quote).toBeHidden();
      // Later viewport events must not resurrect stale text.
      await page.evaluate(() => window.visualViewport.dispatchEvent(new Event("resize")));
      await settleSelection(page);
      await expect(quote).toBeHidden();
      await expect(composer).toHaveValue("Keep this invalidated draft");
      expect(requests).toHaveLength(0);
    });
  }

  test("invalidate touch quotes across session switches without duplicating insertion", async ({ page }, testInfo) => {
    const { persisted, live } = titles[testInfo.project.name];
    await openQuoteSession(page, persisted);
    const url = new URL(page.url());
    url.searchParams.set("session_search", "E2E Quote");
    await page.goto(url.toString());
    await page.evaluate(() => { window.quoteNavigationSentinel = true; });
    const composer = page.getByLabel("Message to Pi");
    await composer.fill("Saved touch draft");
    const requests = recordSendRequests(page);
    const body = message(page, "assistant", `Fixture answer for ${persisted}`).locator(".message-body");
    await selectMobileMessageText(body, "Fixture answer");
    const quote = page.getByRole("button", { name: "Quote selection", exact: true });
    await expect(quote).toBeVisible();
    for (const title of [live, persisted]) {
      await page.locator('label[aria-label="Open sessions"]').tap();
      await page.getByRole("link", { name: new RegExp(title) }).tap();
      await expect(page.getByRole("heading", { level: 1, name: title })).toBeVisible();
      await expect(quote).toBeHidden();
    }
    await expect(composer).toHaveValue("Saved touch draft");
    await selectMobileMessageText(body, "Fixture answer");
    await quote.tap();
    await expect(composer).toHaveValue("Saved touch draft\n\n> Fixture answer\n\n");
    expect(await page.evaluate(() => window.quoteNavigationSentinel)).toBe(true);
    expect(requests).toHaveLength(0);
  });

  test("show a dark, mixed-case Quote action with a 44px touch target", async ({ page }, testInfo) => {
    const title = titles[testInfo.project.name].persisted;
    await openQuoteSession(page, title);
    const body = message(page, "assistant", `Fixture answer for ${title}`).locator(".message-body");
    await selectMobileMessageText(body, "Fixture answer");
    const quote = page.getByRole("button", { name: "Quote selection", exact: true });
    await expect(quote).toBeVisible();
    await expect(quote).toHaveText("Quote selection");
    await expect(quote).toHaveCSS("text-transform", "none");
    await expectTouchQuoteBounds(page, quote);
    const background = await quote.evaluate((element) => {
      // Canvas normalizes CSS color syntax across Chromium and WebKit.
      const context = document.createElement("canvas").getContext("2d");
      context.fillStyle = getComputedStyle(element).backgroundColor;
      context.fillRect(0, 0, 1, 1);
      return [...context.getImageData(0, 0, 1, 1).data];
    });
    expect(background[3]).toBe(255);
    expect(Math.max(...background.slice(0, 3))).toBeLessThan(100);
    await page.screenshot({ path: testInfo.outputPath("quote-touch.png") });
  });
});

async function openQuoteSession(page, title) {
  await page.setViewportSize({ width: 300, height: 700 });
  await page.goto(`/?${new URLSearchParams({ session_search: title })}`);
  await page.locator('label[aria-label="Open sessions"]').tap();
  await page.getByRole("link", { name: new RegExp(title) }).tap();
  await expect(page.getByRole("heading", { level: 1, name: title })).toBeVisible();
  expect(await page.evaluate(() => matchMedia("(pointer: coarse)").matches)).toBe(true);
}

async function selectMobileMessageText(body, text) {
  await body.scrollIntoViewIfNeeded();
  await body.evaluate(async (element, selectedText) => {
    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    const walker = document.createTreeWalker(element, NodeFilter.SHOW_TEXT);
    let node;
    while ((node = walker.nextNode())) {
      const start = node.textContent.indexOf(selectedText);
      if (start === -1) continue;
      const range = document.createRange();
      range.setStart(node, start);
      range.setEnd(node, start + selectedText.length);
      const selection = window.getSelection();
      selection.removeAllRanges();
      selection.addRange(range);
      return;
    }
    throw new Error(`Message text not found: ${selectedText}`);
  }, text);
}

async function touchPointer(target, type) {
  await target.dispatchEvent(type, { pointerId: 1, pointerType: "touch", isPrimary: true, bubbles: true, button: type === "pointerdown" ? 0 : -1, buttons: type === "pointerdown" ? 1 : 0 });
}

async function settleSelection(page) {
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
}

async function expectTouchQuoteBounds(page, quote) {
  const bounds = await quote.boundingBox();
  const composerBounds = await page.locator(".composer").boundingBox();
  const viewport = await page.evaluate(() => ({ left: visualViewport.offsetLeft, top: visualViewport.offsetTop, width: visualViewport.width, height: visualViewport.height }));
  expect(bounds).not.toBeNull();
  expect(composerBounds).not.toBeNull();
  expect(bounds.width).toBeGreaterThanOrEqual(44);
  expect(bounds.height).toBeGreaterThanOrEqual(44);
  expect(bounds.x).toBeGreaterThanOrEqual(viewport.left);
  expect(bounds.x + bounds.width).toBeLessThanOrEqual(viewport.left + viewport.width);
  expect(bounds.y).toBeGreaterThanOrEqual(viewport.top);
  expect(bounds.y + bounds.height).toBeLessThanOrEqual(viewport.top + viewport.height);
  expect(bounds.y + bounds.height).toBeLessThanOrEqual(composerBounds.y);
}

function recordSendRequests(page) {
  const requests = [];
  page.on("request", (request) => {
    if (request.method() === "POST" && /\/(prompt|bash)$/.test(new URL(request.url()).pathname)) requests.push(request);
  });
  return requests;
}
