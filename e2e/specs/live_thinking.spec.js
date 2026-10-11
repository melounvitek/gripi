import { expect, test } from "@playwright/test";
import { live, sessions } from "../support/contract.mjs";
import { continueScenario, expectRunFinished, liveMessage, selectSession, sendPrompt } from "../support/ui.mjs";

const scenario = live.thinking;

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.liveThinking);
});

function liveThinking(page) {
  return page.locator('#live-output article.message--live.message--thinking[data-thinking-index="0"]');
}

test("a matching thinking_end marks live thinking completed", async ({ page }) => {
  await sendPrompt(page, scenario.prompt);
  await expect(page.getByRole("dialog", { name: scenario.gates.thinking })).toBeVisible();
  const thinking = liveThinking(page);
  // An unfinished paragraph streams as the placeholder.
  await expect(thinking).toContainText("Thinking…");
  await expect(thinking).toHaveAttribute("data-activity-active", "");
  await expect(thinking).not.toHaveAttribute("data-activity-completed");

  await continueScenario(page, scenario.gates.thinking);
  await expect(page.getByRole("dialog", { name: scenario.gates.ended })).toBeVisible();
  await expect(thinking).toContainText(scenario.thought);
  await expect(thinking).toHaveAttribute("data-activity-completed", "");
  await expect(thinking).not.toHaveAttribute("data-activity-active");

  await continueScenario(page, scenario.gates.ended);
  await expect(liveMessage(page, "assistant", scenario.reply)).toBeVisible();
  await expect(thinking).toHaveAttribute("data-activity-completed", "");
  await expectRunFinished(page);
});

test("an assistant error while thinking leaves the thought unfinished", async ({ page }) => {
  await sendPrompt(page, scenario.errorPrompt);
  await expect(page.getByRole("dialog", { name: scenario.gates.failing })).toBeVisible();
  const thinking = liveThinking(page);
  await expect(thinking).toHaveAttribute("data-activity-active", "");

  await continueScenario(page, scenario.gates.failing);
  await expect(liveMessage(page, "error", scenario.error)).toBeVisible();
  await expect(thinking).toHaveCount(1);
  await expect(thinking).not.toHaveAttribute("data-activity-active");
  await expect(thinking).not.toHaveAttribute("data-activity-completed");
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "error");
});

test("stopping Pi while thinking leaves the thought unfinished", async ({ page }) => {
  await sendPrompt(page, scenario.abortPrompt);
  const thinking = liveThinking(page);
  await expect(thinking).toHaveAttribute("data-activity-active", "");

  await page.getByRole("button", { name: "Abort running Pi" }).click();
  await expect(thinking).not.toHaveAttribute("data-activity-active");
  await expect(thinking).toHaveCount(1);
  await expect(thinking).not.toHaveAttribute("data-activity-completed");
  await expectRunFinished(page);
  await expect(page.locator('#live-output article.message--live[data-role="error"]')).toHaveCount(0);
});
