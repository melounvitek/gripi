import { expect, test } from "@playwright/test";
import { prompts, sessions, subagents } from "../support/contract.mjs";
import { expectRunFinished, selectSession, sendPrompt } from "../support/ui.mjs";

test("keep a completed parallel subagent visible while its sibling runs", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.parallelSubagents);
  await sendPrompt(page, prompts.parallelSubagents);

  const first = page.locator(`article[data-tool-call-id="${subagents.firstCallId}"]`);
  const second = page.locator(`article[data-tool-call-id="${subagents.secondCallId}"]`);
  await expect(first).toContainText(subagents.firstResult);
  await expect(second).toContainText(subagents.secondProgress);
  await expect(page.getByRole("button", { name: "Abort running Pi" })).toBeVisible();

  await page.reload();
  await expect(page.getByRole("heading", { level: 1, name: sessions.parallelSubagents })).toBeVisible();
  await expect(first).toHaveCount(1);
  await expect(first).toContainText(subagents.firstResult);
  await expect(second).toHaveCount(1);
  await expect(second).toContainText(subagents.secondProgress);

  await page.getByRole("button", { name: "Abort running Pi" }).click();
  await expectRunFinished(page);
  await expect(first).toHaveCount(1);
  await expect(second).toHaveCount(1);

  await page.reload();
  await expect(first).toHaveCount(1);
  await expect(first).toContainText(subagents.firstResult);
  await expect(second).toHaveCount(1);
  await expect(second).toContainText(subagents.secondResult);
});
