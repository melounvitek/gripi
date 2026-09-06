import { expect, test } from "@playwright/test";
import { prompts, replies, sessions } from "../support/contract.mjs";
import { message, selectSession, sendPrompt } from "../support/ui.mjs";

test("polling after sleep restores completed compaction without a browser wake event", async ({ page }, testInfo) => {
  await page.goto("/");
  await selectSession(page, sessions.sleepRecovery);
  await page.getByLabel("Message to Pi").fill("/compact");
  await page.locator(".prompt-form").evaluate((form) => form.requestSubmit());
  await expect(page.locator(".composer-state")).toContainText("Compacting…");
  await sendPrompt(page, prompts.standard);
  await expect(page.locator(".pending-message--steering")).toContainText(prompts.standard);

  const draft = "Keep my unsent message after waking";
  await page.getByLabel("Message to Pi").fill(draft);
  await page.setViewportSize({ width: 1000, height: 600 });
  const conversation = page.locator("#conversation-scroll");
  await conversation.hover();
  await page.mouse.wheel(0, -2000);
  await expect.poll(() => conversation.evaluate((element) => element.scrollTop)).toBe(0);
  const now = await page.evaluate(() => Date.now());
  await page.clock.install({ time: now });
  await page.clock.pauseAt(now + 100);
  // An empty successful batch must not mask the gap while the browser was asleep.
  await page.route(/\/events(?:\?|$)/, (route) => route.fulfill({ json: { events: [], last_seq: 0, missed: false } }));

  const fragmentUrl = new URL("/session_fragment", page.url());
  fragmentUrl.searchParams.set("session", new URL(page.url()).searchParams.get("session"));
  await expect.poll(async () => {
    const response = await page.request.get(fragmentUrl.toString());
    const html = (await response.json()).conversation_html;
    return html.includes(replies.standard) && html.includes('data-composer-state="idle"');
  }).toBe(true);
  await expect(page.locator(".composer-state")).toContainText("Compacting…");

  let refreshes = 0;
  page.on("request", (request) => {
    if (new URL(request.url()).pathname === "/session_fragment") refreshes += 1;
  });
  await page.clock.setSystemTime(now + 61_000);
  await page.clock.runFor(1000);

  await expect(message(page, "assistant", replies.standard)).toBeVisible();
  await expect(page.getByRole("button", { name: "Abort running Pi" })).toBeHidden();
  await expect(page.locator(".pending-message--steering")).toHaveCount(0);
  await expect(page.getByLabel("Message to Pi")).toHaveValue(draft);
  await expect(page.getByText("Session may be stale.")).toBeHidden();
  await page.clock.runFor(2000);
  await expect.poll(() => conversation.evaluate((element) => element.scrollTop)).toBe(0);
  expect(refreshes).toBe(1);
  await message(page, "assistant", replies.standard).scrollIntoViewIfNeeded();
  await page.screenshot({ path: testInfo.outputPath("recovered-session.png") });
});

test("failed wake recovery keeps the draft and warning while backing off before retrying", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.history);
  const draft = "Keep this draft while reconnecting";
  await page.getByLabel("Message to Pi").fill(draft);
  const now = await page.evaluate(() => Date.now());
  await page.clock.install({ time: now });
  await page.clock.pauseAt(now + 100);
  await page.route(/\/events(?:\?|$)/, (route) => route.fulfill({ json: { events: [], last_seq: 0, missed: false } }));

  let refreshes = 0;
  let available = false;
  await page.route(/\/session_fragment(?:\?|$)/, async (route) => {
    refreshes += 1;
    if (available) await route.continue();
    else await route.fulfill({ status: 503, body: "Temporarily unavailable" });
  });
  await page.clock.setSystemTime(now + 61_000);
  await page.clock.resume();

  await expect(page.getByText("Session may be stale.")).toBeVisible();
  await expect(page.getByLabel("Message to Pi")).toHaveValue(draft);
  await page.waitForTimeout(1000);
  expect(refreshes).toBe(1);

  available = true;
  await expect(page.getByText("Session may be stale.")).toBeHidden();
  await expect(page.getByLabel("Message to Pi")).toHaveValue(draft);
  expect(refreshes).toBe(2);
});
