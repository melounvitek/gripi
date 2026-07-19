import { expect, test } from "@playwright/test";
import { prompts, replies } from "../support/contract.mjs";
import { expectRunFinished, message, sendPrompt } from "../support/ui.mjs";

test("start a session in a configured directory and persist its first response", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("button", { name: "New session" }).click();

  const dialog = page.getByRole("dialog", { name: "New session" });
  await expect(dialog).toBeVisible();
  await dialog.getByRole("combobox", { name: "Project" }).click();
  await page.getByRole("option", { name: /new-session-desktop/ }).click();
  await dialog.getByRole("button", { name: "Start session" }).click();
  await expect(page.getByRole("heading", { level: 1, name: "New session (pending first assistant response)" })).toBeVisible();

  await sendPrompt(page, prompts.newSession);
  await expect(message(page, "assistant", replies.newSession)).toBeVisible();
  await expectRunFinished(page);

  await page.reload();
  await expect(page.getByRole("heading", { level: 1, name: prompts.newSession })).toBeVisible();
  await expect(message(page, "user", prompts.newSession)).toBeVisible();
  await expect(message(page, "assistant", replies.newSession)).toBeVisible();
});
