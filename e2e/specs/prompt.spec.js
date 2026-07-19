import { expect, test } from "@playwright/test";
import { prompts, replies, sessions, tool } from "../support/contract.mjs";
import { expectRunFinished, message, selectSession, sendPrompt } from "../support/ui.mjs";

test("stream a tool-backed answer and render the persisted result after reload", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.prompt);

  await sendPrompt(page, prompts.standard);
  await expect(message(page, "user", prompts.standard)).toBeVisible();
  await expect(message(page, "assistant", tool.command)).toBeVisible();
  await expect(page.getByRole("article").filter({ hasText: tool.result })).toBeVisible();
  await expect(message(page, "assistant", replies.standard)).toBeVisible();
  await expectRunFinished(page);

  await page.reload();
  await expect(page.getByRole("heading", { level: 1, name: sessions.prompt })).toBeVisible();
  await expect(message(page, "user", prompts.standard)).toBeVisible();
  await expect(page.getByRole("article").filter({ hasText: tool.command }).filter({ hasText: tool.result })).toBeVisible();
  await expect(message(page, "assistant", replies.standard)).toBeVisible();
});
