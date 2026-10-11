import { expect, test } from "@playwright/test";
import { live, sessions } from "../support/contract.mjs";
import { continueScenario, expectRunFinished, liveMessage, selectSession, sendPrompt } from "../support/ui.mjs";

const scenario = live.compaction;

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.liveCompaction);
});

function pendingCompaction(page) {
  return page.locator('#live-output [data-pending-compaction="true"]');
}

function compacted(page) {
  return page.locator("#live-output article.message--live.message--compaction:not([data-pending-compaction])");
}

test("a failed automatic compaction replaces the pending card with its error", async ({ page }) => {
  await sendPrompt(page, scenario.failurePrompt);
  await expect(page.getByRole("dialog", { name: scenario.failureGate })).toBeVisible();
  await expect(pendingCompaction(page)).toHaveCount(1);
  await expect(page.locator(".composer-state")).toHaveText(/^Compacting…/);

  await continueScenario(page, scenario.failureGate);
  await expect(liveMessage(page, "error", scenario.error)).toHaveCount(1);
  await expect(pendingCompaction(page)).toHaveCount(0);
  await expect(compacted(page)).toHaveCount(0);
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "error");
  await expect(page.locator(".composer-state")).toHaveText(scenario.error);
});

test("stopping an automatic compaction removes the pending card without a result", async ({ page }) => {
  await sendPrompt(page, scenario.abortPrompt);
  await expect(pendingCompaction(page)).toHaveCount(1);
  await expect(page.locator(".composer-state")).toHaveText(/^Compacting…/);

  await page.getByRole("button", { name: "Abort running Pi" }).click();
  await expectRunFinished(page);
  await expect(pendingCompaction(page)).toHaveCount(0);
  await expect(compacted(page)).toHaveCount(0);
  await expect(page.locator('#live-output article.message--live[data-role="error"]')).toHaveCount(0);
});

test("a compaction summary repeated within 10 s shows once", async ({ page }) => {
  await sendPrompt(page, scenario.duplicatePrompt);
  await expect(page.getByRole("dialog", { name: scenario.duplicateGate })).toBeVisible();
  // compaction_end and a legacy compaction event at once share a card; the one 11 s later adds another.
  await expect(compacted(page)).toHaveCount(2);
  for (const card of await compacted(page).all()) {
    await expect(card).toContainText("Conversation compacted");
    await expect(card).toContainText(scenario.summary);
  }
  await expect(pendingCompaction(page)).toHaveCount(0);

  await continueScenario(page, scenario.duplicateGate);
  await expectRunFinished(page);
});
