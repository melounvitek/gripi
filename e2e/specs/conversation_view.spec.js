import { expect, test } from "@playwright/test";
import { prompts, sessions, tool } from "../support/contract.mjs";
import { expectRunFinished, message, selectSession, sendPrompt } from "../support/ui.mjs";

test("toggles transcript details with a single button", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.toolSummary);
  await sendPrompt(page, prompts.longCommand);

  const toolCall = message(page, "assistant", `$ ${tool.longCommand}`).last();
  await expect(toolCall).toBeVisible();

  const toggle = page.getByRole("button", { name: "Messages-only transcript view" });
  await expect(toggle).toHaveAttribute("aria-pressed", "false");
  await expect(toggle).toHaveAttribute("title", "Show messages only");
  await expect(toggle.locator('[data-view-icon="full"]')).toBeVisible();
  await expect(toggle.locator('[data-view-icon="messages"]')).toBeHidden();
  await expect(toggle.getByText("All", { exact: true })).toBeVisible();
  await expect(toggle.getByText("Chat", { exact: true })).toBeHidden();
  await toggle.click();

  await expect(page.locator(".conversation-panel")).toHaveClass(/is-conversation-focused/);
  await expect(toggle).toHaveAttribute("aria-pressed", "true");
  await expect(toggle).toHaveAttribute("title", "Show all details");
  await expect(toggle.locator('[data-view-icon="messages"]')).toBeVisible();
  await expect(toggle.locator('[data-view-icon="full"]')).toBeHidden();
  await expect(toggle.getByText("Chat", { exact: true })).toBeVisible();
  await expect(toggle.getByText("All", { exact: true })).toBeHidden();
  await expect(toolCall).toBeHidden();

  await toggle.click();
  await expect(page.locator(".conversation-panel")).not.toHaveClass(/is-conversation-focused/);
  await expect(toggle).toHaveAttribute("aria-pressed", "false");
  await expect(toolCall).toBeVisible();
  await expectRunFinished(page);

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
