import { expect, test } from "@playwright/test";
import { hermes } from "../support/contract.mjs";
import { expectRunFinished, sendPrompt } from "../support/ui.mjs";

test("a new Hermes session without a folder starts from the dialog on the first taps", async ({ page }) => {
  await page.goto("/");
  await page.locator('label[aria-label="Open sessions"]').tap();
  await page.getByRole("button", { name: "New session", exact: true }).tap();
  const dialog = page.getByRole("dialog", { name: "New session", exact: true });
  await expect(dialog.getByRole("option", { name: /^pi\b/ })).toBeVisible();

  await dialog.getByRole("option", { name: /hermes/ }).tap();
  await dialog.getByRole("option", { name: /No folder/ }).tap();
  await expect(page.getByRole("heading", { level: 1, name: "New session (pending first assistant response)" })).toBeVisible();
  await expect(page.locator(".session-header-agent")).toHaveText("hermes");

  await sendPrompt(page, "Say hello from a phone");
  await expect(page.locator('#live-output article[data-role="assistant"]').filter({ hasText: `${hermes.run.replyPrefix}Say hello from a phone` })).toBeVisible();
  await expectRunFinished(page);
});
