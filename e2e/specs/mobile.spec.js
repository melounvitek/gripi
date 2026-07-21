import { expect, test } from "@playwright/test";
import { nativeBash, prompts, replies, sessions } from "../support/contract.mjs";
import { expectRunFinished, message, sendPrompt } from "../support/ui.mjs";

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
  await search.fill("History Desktop");
  await Promise.all([
    page.waitForURL((url) => url.searchParams.get("session_search") === "History Desktop"),
    search.press("Enter")
  ]);

  await expect(drawerToggle).toBeChecked();
  await expect(page.getByRole("link", { name: new RegExp(sessions.history) })).toBeVisible();
});

test("navigate and complete a conversation from the mobile session drawer", async ({ page }) => {
  await page.goto("/");

  await page.locator('label[aria-label="Open sessions"]').click();
  await expect(page.getByRole("complementary", { name: "Sessions" })).toBeVisible();
  await page.getByRole("link", { name: new RegExp(sessions.mobile) }).click();
  await expect(page.getByRole("heading", { level: 1, name: sessions.mobile })).toBeVisible();
  await expect(page.locator("#mobile-session-toggle")).not.toBeChecked();

  await sendPrompt(page, prompts.standard);
  await expect(message(page, "assistant", replies.standard)).toBeVisible();
  await expectRunFinished(page);

  await page.reload();
  await expect(page.getByRole("heading", { level: 1, name: sessions.mobile })).toBeVisible();
  await expect(message(page, "user", prompts.standard)).toBeVisible();
  await expect(message(page, "assistant", replies.standard)).toBeVisible();
});

test("cancel a native bash command on the first mobile tap", async ({ page }) => {
  await page.goto("/");

  await page.locator('label[aria-label="Open sessions"]').click();
  await page.getByRole("link", { name: new RegExp(sessions.bashMobile) }).click();
  await expect(page.getByRole("heading", { level: 1, name: sessions.bashMobile })).toBeVisible();

  await sendPrompt(page, `!${nativeBash.mobileCancel.command}`);
  const card = page.locator('article[data-role="bashExecution"]').filter({ hasText: `$ ${nativeBash.mobileCancel.command}` });
  await expect(card.getByRole("status", { name: "Shell command status" })).toContainText("running");

  await page.getByRole("button", { name: "Abort running Pi" }).tap();

  await expect(card).toHaveClass(/message--bash-cancelled/);
  await expect(card.getByRole("status", { name: "Shell command status" })).toContainText("cancelled");
  await expectRunFinished(page);
});
