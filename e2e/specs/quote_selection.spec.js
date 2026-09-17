import { expect, test } from "@playwright/test";
import { prompts, replies, sessions } from "../support/contract.mjs";
import { expectRunFinished, message, selectSession, sendPrompt } from "../support/ui.mjs";

async function selectText(body) {
  await body.scrollIntoViewIfNeeded();
  return body.evaluate((element) => {
    const range = document.createRange();
    range.selectNodeContents(element);
    const selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
    return selection.toString();
  });
}

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.prompt);
});

test("quotes history without replacing drafts or attachments, sending, or losing the saved draft", async ({ page }) => {
  const composer = page.getByLabel("Message to Pi");
  await composer.fill("My existing question  ");
  await page.locator("#image-input").setInputFiles({ name: "quote.png", mimeType: "image/png", buffer: await page.screenshot() });
  const attachment = page.locator(".attachment-tray");
  await expect(attachment).toContainText("quote.png");
  const requests = [];
  page.on("request", (request) => {
    if (["/prompt", "/bash"].includes(new URL(request.url()).pathname)) requests.push(request);
  });
  const body = message(page, "assistant", `Fixture answer for ${sessions.prompt}`).locator(".message-body");
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

test("quotes live messages and their reloaded history, with keyboard activation", async ({ page }) => {
  await sendPrompt(page, prompts.standard);
  await expectRunFinished(page);
  const body = message(page, "assistant", replies.standard).last().locator(".message-body");
  const quote = page.getByRole("button", { name: "Quote selection", exact: true });
  for (const reload of [false, true]) {
    if (reload) await page.reload();
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
  await sendPrompt(page, prompts.markdownFence);
  await expectRunFinished(page);
  const body = message(page, "assistant", "Modal fence stays in conversation").last().locator(".message-body");
  const text = await selectText(body);
  const quote = page.getByRole("button", { name: "Quote selection", exact: true });
  await expect(quote).toBeVisible();
  await quote.click();
  expect(text).not.toContain("Copy");
  expect(text).toContain("const safeValue = 42;");
  await expect(page.getByLabel("Message to Pi")).toHaveValue(text.split("\n").map((line) => `> ${line}`).join("\n") + "\n\n");
});

test("quotes a mouse-drag selection", async ({ page }) => {
  const body = message(page, "assistant", `Fixture answer for ${sessions.prompt}`).locator(".message-body");
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
  const url = new URL(page.url());
  url.searchParams.delete("session_search");
  url.searchParams.set("sidebar_sessions_limit", "100");
  await page.goto(url.toString());
  await page.evaluate(() => { window.quoteNavigationSentinel = true; });
  const composer = page.getByLabel("Message to Pi");
  await composer.fill("Saved draft");
  const body = message(page, "assistant", `Fixture answer for ${sessions.prompt}`).locator(".message-body");
  const quote = page.getByRole("button", { name: "Quote selection", exact: true });
  await selectText(body);
  await expect(quote).toBeVisible();
  await selectSession(page, sessions.history);
  await expect(quote).toBeHidden();
  await expect(composer).toHaveValue("");
  await selectSession(page, sessions.prompt);
  await expect(composer).toHaveValue("Saved draft");
  await expect(quote).toBeHidden();
  const text = await selectText(body);
  await quote.click();
  await expect(composer).toHaveValue(`Saved draft\n\n> ${text}\n\n`);
  expect(await page.evaluate(() => window.quoteNavigationSentinel)).toBe(true);
});

test("does not insert into a composer that becomes disabled", async ({ page }) => {
  const composer = page.getByLabel("Message to Pi");
  await composer.fill("Keep this");
  const body = message(page, "assistant", `Fixture answer for ${sessions.prompt}`).locator(".message-body");
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
  const composer = page.getByLabel("Message to Pi");
  await composer.fill("Keep this");
  const body = message(page, "assistant", `Fixture answer for ${sessions.prompt}`).locator(".message-body");
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
  await expect(composer).toHaveValue("Keep this");
});
