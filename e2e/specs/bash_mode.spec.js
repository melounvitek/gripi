import { expect, test } from "@playwright/test";
import { nativeBash, prompts, sessions } from "../support/contract.mjs";
import { expectRunFinished, message, selectSession, sendPrompt } from "../support/ui.mjs";

test("automatically retries transient contention for native bash", async ({ page }) => {
  const command = "printf 'retried native bash'";
  let promptRequests = 0;
  await page.route("**/prompt", async (route) => {
    promptRequests += 1;
    if (promptRequests === 1) {
      await route.fulfill({
        status: 409,
        contentType: "application/json",
        body: JSON.stringify({ code: "session_operation_pending" })
      });
      return;
    }

    await route.continue();
  });

  await page.goto("/");
  await selectSession(page, sessions.bashRetry);
  await sendPrompt(page, `!${command}`);

  await expect(bashCard(page, command)).toContainText(`Fake Pi completed: ${command}`);
  expect(promptRequests).toBeGreaterThanOrEqual(2);
});

test("complete an included native bash command and restore it after reload", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.bashIncluded);

  await sendPrompt(page, `!${nativeBash.included.command}`);
  const card = bashCard(page, nativeBash.included.command);
  await expectLongOutputCollapsed(card);
  await expect(card).toHaveAttribute("data-role", "bashExecution");
  await expect(card.locator(".bash-execution-status-item")).toHaveCount(0);
  await card.getByRole("button", { name: "Expand" }).click();
  await expect(card.getByRole("region", { name: "Expanded tool output" })).toContainText(nativeBash.included.output.split("\n")[0]);
  await expectRunFinished(page);

  await page.reload();
  await expect(page.getByRole("heading", { level: 1, name: sessions.bashIncluded })).toBeVisible();
  const restored = bashCard(page, nativeBash.included.command);
  await expect(restored).toHaveCount(1);
  await expectLongOutputCollapsed(restored);
});

test("mark a double-bang command as excluded from model context", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.bashExcluded);

  await sendPrompt(page, `!!${nativeBash.excluded.command}`);
  const card = bashCard(page, nativeBash.excluded.command);
  await expect(card).toContainText(nativeBash.excluded.output.trim());
  await expect(card).toHaveClass(/message--bash-excluded/);
  await expect(card.getByRole("status", { name: "Shell command status" })).toContainText("excluded from model context");
  await expectRunFinished(page);

  await page.reload();
  await expect(card).toHaveCount(1);
  await expect(card).toHaveClass(/message--bash-excluded/);
  await expect(card.getByRole("status", { name: "Shell command status" })).toContainText("excluded from model context");
});

test("cancel a long-running bash command with one click", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.bashCancel);

  await sendPrompt(page, `!${nativeBash.cancel.command}`);
  const card = bashCard(page, nativeBash.cancel.command);
  await expect(card.getByRole("status", { name: "Shell command status" })).toContainText("running");

  await page.getByLabel("Message to Pi").fill("/logout");
  await page.locator(".prompt-form").evaluate((form) => form.requestSubmit());
  await expect(message(page, "gateway", "restart the Gripi gateway")).toBeVisible();
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "bash");

  await page.reload();
  await expect(card).toHaveCount(1);
  await expect(card.getByRole("status", { name: "Shell command status" })).toContainText("running");
  await page.getByRole("button", { name: "Abort running Pi" }).click();

  await expect(card).toHaveClass(/message--bash-cancelled/);
  await expect(card.getByRole("status", { name: "Shell command status" })).toContainText("cancelled");
  await expectRunFinished(page);
});

test("stop overlapping bash before retaining and then aborting the agent run", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.bashOverlap);
  await sendPrompt(page, prompts.abortStart);
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "running");

  await sendPrompt(page, `!${nativeBash.overlap.command}`);
  const card = bashCard(page, nativeBash.overlap.command);
  await expect(card.getByRole("status", { name: "Shell command status" })).toContainText("running");

  const stop = page.getByRole("button", { name: "Abort running Pi" });
  await stop.click();
  await expect(card.getByRole("status", { name: "Shell command status" })).toContainText("cancelled");
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "running");
  await expect(page.getByRole("button", { name: "Send steer" })).toBeVisible();
  await expect(page.getByLabel("Message to Pi")).toHaveAttribute("placeholder", "Steer Pi…");
  await expect(stop).toBeEnabled();

  await stop.click();
  await expectRunFinished(page);

  await page.reload();
  const restored = bashCard(page, nativeBash.overlap.command);
  await expect(restored).toHaveCount(1);
  await expect(restored).toHaveClass(/message--bash-cancelled/);
  await expect(restored.getByRole("status", { name: "Shell command status" })).toContainText("cancelled");
});

function bashCard(page, command) {
  return page.locator('article[data-role="bashExecution"]').filter({ hasText: `$ ${command}` });
}

async function expectLongOutputCollapsed(card) {
  const lines = nativeBash.included.output.trimEnd().split("\n");
  await expect(card.getByRole("button", { name: "Expand" })).toBeVisible();
  await expect(card.locator(".message-body").getByText(lines[0], { exact: true })).toHaveCount(0);
  await expect(card.locator(".message-body").getByText(lines.at(-1), { exact: true })).toBeVisible();
}
