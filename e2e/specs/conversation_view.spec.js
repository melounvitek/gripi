import { expect, test } from "@playwright/test";
import { prompts, sessions, tool } from "../support/contract.mjs";
import { expectRunFinished, message, selectSession, sendPrompt } from "../support/ui.mjs";

test("toggles transcript details with a single icon button", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.toolSummary);
  await sendPrompt(page, prompts.longCommand);

  const toolCall = message(page, "assistant", `$ ${tool.longCommand}`).last();
  await expect(toolCall).toBeVisible();

  const showMessagesOnly = page.getByRole("button", { name: "Show messages only" });
  await expect(showMessagesOnly).toHaveAttribute("aria-pressed", "false");
  await expect(showMessagesOnly).toHaveAttribute("title", "Show messages only");
  await expect(showMessagesOnly.locator('[data-view-icon="full"]')).toBeVisible();
  await expect(showMessagesOnly.locator('[data-view-icon="messages"]')).toBeHidden();
  await showMessagesOnly.click();

  const showAllDetails = page.getByRole("button", { name: "Show all details" });
  await expect(page.locator(".conversation-panel")).toHaveClass(/is-conversation-focused/);
  await expect(showAllDetails).toHaveAttribute("aria-pressed", "true");
  await expect(showAllDetails).toHaveAttribute("title", "Show all details");
  await expect(showAllDetails.locator('[data-view-icon="messages"]')).toBeVisible();
  await expect(showAllDetails.locator('[data-view-icon="full"]')).toBeHidden();
  await expect(toolCall).toBeHidden();

  await showAllDetails.click();
  await expect(page.locator(".conversation-panel")).not.toHaveClass(/is-conversation-focused/);
  await expect(page.getByRole("button", { name: "Show messages only" })).toHaveAttribute("aria-pressed", "false");
  await expect(toolCall).toBeVisible();

  await expectRunFinished(page);
});
