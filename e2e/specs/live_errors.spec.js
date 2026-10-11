import { expect, test } from "@playwright/test";
import { live, sessions } from "../support/contract.mjs";
import { continueScenario, expectRunFinished, liveMessage, selectSession, sendPrompt } from "../support/ui.mjs";

// Events with an error field, or an error-like type, fall through to an error card and the composer's error state.
const { transientRetry, exhaustedRetry, extensionErrors, summarizationRetry } = live.errors;

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.liveErrors);
});

test("a transient auto-retry shows its error but settles as done", async ({ page }) => {
  await sendPrompt(page, transientRetry.prompt);
  await expect(page.getByRole("dialog", { name: transientRetry.gate })).toBeVisible();
  // The failed attempt's message_end and auto_retry_start each add a card.
  await expect(liveMessage(page, "error", transientRetry.error)).toHaveCount(2);
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "running");

  await continueScenario(page, transientRetry.gate);
  await expect(liveMessage(page, "assistant", transientRetry.reply)).toBeVisible();
  await expectRunFinished(page);
  await expect(liveMessage(page, "error", transientRetry.error)).toHaveCount(2);
});

test("exhausted auto-retries leave the final error in the composer", async ({ page }) => {
  await sendPrompt(page, exhaustedRetry.prompt);
  // The failed message_end and auto_retry_end's finalError each add a card.
  await expect(liveMessage(page, "error", exhaustedRetry.error)).toHaveCount(2);
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "error");
  await expect(page.locator(".composer-state")).toHaveText(exhaustedRetry.error);
});

test("extension errors show as error cards and outlast a successful reply", async ({ page }) => {
  await sendPrompt(page, extensionErrors.prompt);
  await expect(liveMessage(page, "assistant", extensionErrors.reply)).toBeVisible();
  await expect(liveMessage(page, "error", extensionErrors.error)).toHaveCount(1);
  // The /sessions command's terminal UI failure gets Gripi's own message.
  await expect(liveMessage(page, "error", extensionErrors.terminalUiError)).toHaveCount(1);
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "error");
  await expect(page.locator(".composer-state")).toHaveText(extensionErrors.terminalUiError);
});

test("a summarization retry stays an error after compaction succeeds", async ({ page }) => {
  await sendPrompt(page, summarizationRetry.prompt);
  await expect(liveMessage(page, "error", summarizationRetry.error)).toHaveCount(1);
  const compacted = page.locator("#live-output article.message--live.message--compaction:not([data-pending-compaction])");
  await expect(compacted).toHaveCount(1);
  await expect(compacted).toContainText("Conversation compacted");
  await expect(compacted).toContainText(summarizationRetry.summary);
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "error");
  await expect(page.locator(".composer-state")).toHaveText(summarizationRetry.error);
});
