import { expect, test } from "@playwright/test";
import { prompts, replies, sessions, tool } from "../support/contract.mjs";
import { expectRunFinished, message, selectSession, sendPrompt } from "../support/ui.mjs";

test("automatically retries transient session contention", async ({ page }) => {
  let promptRequests = 0;
  await page.route("**/prompt", async (route) => {
    promptRequests += 1;
    if (promptRequests === 1) {
      await route.fulfill({
        status: 409,
        contentType: "application/json",
        body: JSON.stringify({
          code: "session_operation_pending",
          error: "Another session operation is pending. Please retry."
        })
      });
      return;
    }

    await new Promise((resolve) => setTimeout(resolve, 500));
    await route.continue();
  });

  await page.goto("/");
  await selectSession(page, sessions.promptRetry);
  await sendPrompt(page, prompts.retry);

  await expect(page.locator(".composer-state")).toHaveText("Waiting to send…");
  await expect(message(page, "assistant", replies.standard)).toBeVisible();
  await expectRunFinished(page);
  await expect(message(page, "user", prompts.retry)).toHaveCount(1);
  expect(promptRequests).toBe(2);
});

test("stops a delayed prompt retry when the user stops", async ({ page }) => {
  let promptRequests = 0;
  await page.route("**/prompt", async (route) => {
    promptRequests += 1;
    await route.fulfill({
      status: 409,
      contentType: "application/json",
      body: JSON.stringify({ code: "session_operation_pending" })
    });
  });

  await page.goto("/");
  await selectSession(page, sessions.promptRetry);
  await sendPrompt(page, prompts.retryCancelled);
  await expect(page.locator(".composer-state")).toHaveText("Waiting to send…");
  await page.getByRole("button", { name: "Abort running Pi" }).click();
  await page.waitForTimeout(500);

  expect(promptRequests).toBe(1);
  await expect(message(page, "user", prompts.retryCancelled)).toHaveCount(0);
});

test("restores and reuses the prompt after contention persists", async ({ page }) => {
  let promptRequests = 0;
  await page.route("**/prompt", async (route) => {
    promptRequests += 1;
    if (promptRequests <= 4) {
      await route.fulfill({
        status: 409,
        contentType: "application/json",
        body: JSON.stringify({
          code: "session_operation_pending",
          error: "Another session operation is pending. Please retry."
        })
      });
      return;
    }

    await route.continue();
  });

  await page.goto("/");
  await selectSession(page, sessions.promptRetry);
  await sendPrompt(page, prompts.retryExhausted);

  await expect(message(page, "assistant", "Another session operation is pending. Please retry.")).toBeVisible();
  await expect(page.getByLabel("Message to Pi")).toHaveValue(prompts.retryExhausted);
  await sendPrompt(page, prompts.retryExhausted);
  await expect(message(page, "assistant", replies.standard)).toBeVisible();
  await expectRunFinished(page);
  await expect(message(page, "user", prompts.retryExhausted)).toHaveCount(1);
  expect(promptRequests).toBe(5);
});

test("clears pending compaction UI after retry exhaustion", async ({ page }) => {
  await page.route("**/prompt", async (route) => {
    await route.fulfill({
      status: 409,
      contentType: "application/json",
      body: JSON.stringify({
        code: "session_operation_pending",
        error: "Another session operation is pending. Please retry."
      })
    });
  });

  await page.goto("/");
  await selectSession(page, sessions.promptRetry);
  const composer = page.getByLabel("Message to Pi");
  await composer.fill("/compact");
  await page.locator(".prompt-form").evaluate((form) => form.requestSubmit());

  await expect(message(page, "assistant", "Another session operation is pending. Please retry.")).toBeVisible();
  await expect(page.getByLabel("Message to Pi")).toHaveValue("/compact");
  await expect(page.locator('[data-pending-compaction="true"]')).toHaveCount(0);
});

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
