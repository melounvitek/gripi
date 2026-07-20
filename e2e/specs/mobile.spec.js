import { expect, test } from "@playwright/test";
import { nativeBash, prompts, replies, sessions } from "../support/contract.mjs";
import { expectRunFinished, message, sendPrompt } from "../support/ui.mjs";

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
