import { expect, test } from "@playwright/test";
import { prompts, replies, sessions } from "../support/contract.mjs";
import { expectRunFinished, message, selectSession, sendPrompt } from "../support/ui.mjs";

test("answer an extension confirmation before Pi completes", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.extension);
  await sendPrompt(page, prompts.extension);

  const dialog = page.getByRole("dialog", { name: "Approve release?" });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByText("Allow the deterministic release?", { exact: true })).toBeVisible();
  await dialog.getByRole("button", { name: "Confirm" }).click();

  await expect(message(page, "assistant", replies.extensionApproved)).toBeVisible();
  await expectRunFinished(page);
});
