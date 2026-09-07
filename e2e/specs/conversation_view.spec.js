import { expect, test } from "@playwright/test";
import { prompts, sessions, tool } from "../support/contract.mjs";
import { expectRunFinished, message, selectSession, sendPrompt } from "../support/ui.mjs";

test("shows one tool result after reloading an active command", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.toolReload);
  await sendPrompt(page, prompts.longCommand);
  await expect(message(page, "assistant", tool.longCommand)).toBeVisible();

  await page.reload();

  await expect(message(page, "toolResult", tool.result)).toHaveCount(1);
  await expectRunFinished(page);
});

test("shows agent activity with an accessible switch", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.toolSummary);
  const toolCalls = message(page, "assistant", `$ ${tool.longCommand}`);
  const previousCount = await toolCalls.count();
  await sendPrompt(page, prompts.longCommand);

  const toolCall = toolCalls.nth(previousCount);
  await expect(toolCall).toBeVisible();

  const toggle = page.getByRole("switch", { name: "Show agent activity" });
  await expect(toggle).toBeChecked();
  await expect(toggle).toHaveText("Show agent activity");
  await toggle.click();

  await expect(page.locator(".conversation-panel")).toHaveClass(/is-conversation-focused/);
  await expect(toggle).not.toBeChecked();
  await expect(toggle).toHaveText("Show agent activity");
  await expect(toolCall).toBeHidden();

  await toggle.click();
  await expect(page.locator(".conversation-panel")).not.toHaveClass(/is-conversation-focused/);
  await expect(toggle).toBeChecked();
  await expect(toolCall).toBeVisible();
  await expectRunFinished(page);

  await page.reload();
  await expect(toggle).toBeChecked();
  await expect(toolCall).toBeVisible();
  await toggle.focus();
  await page.keyboard.press("Space");
  await expect(toggle).not.toBeChecked();
  await expect(toolCall).toBeHidden();
  await page.keyboard.press("Enter");
  await expect(toggle).toBeChecked();
  await expect(toolCall).toBeVisible();

  await page.keyboard.press("Control+f");
  const find = page.getByRole("searchbox", { name: "Find in conversation" });
  await find.fill("deterministic-tool-result");
  const count = page.locator("[data-current-session-find-count]");
  await expect(count).toHaveText("1 / 1");

  await toggle.click();
  await expect(count).toHaveText("0 / 0");
  await toggle.click();
  await expect(count).toHaveText("1 / 1");
});
