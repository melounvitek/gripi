import { expect, test } from "@playwright/test";
import { prompts, sessions, tool } from "../support/contract.mjs";
import { expectRunFinished, message, selectSession, sendPrompt } from "../support/ui.mjs";

test.use({ hasTouch: true });

test("show the full wrapped tool command live and after reload", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.toolSummary);
  await sendPrompt(page, prompts.longCommand);

  const card = message(page, "assistant", "pi --no-session").last();
  await expectFullCommand(card);

  await page.setViewportSize({ width: 390, height: 844 });
  await expectFullCommand(card, { wrapped: true });
  await showMessagesOnly(page);
  const activity = await activityFor(page, card);
  await expectCollapsedActivity(activity);
  await activity.locator("[data-focus-activity-toggle]").tap();
  await expectExpandedActivity(activity);
  await expectRunFinished(page);
  await expectExpandedActivity(activity);

  await page.reload();
  const restoredCard = message(page, "assistant", "pi --no-session").last();
  await expectFullCommand(restoredCard, { wrapped: true });
  await showMessagesOnly(page);
  const restoredActivity = await activityFor(page, restoredCard);
  await expectCollapsedActivity(restoredActivity);
  await restoredActivity.locator("[data-focus-activity-toggle]").tap();
  await expectExpandedActivity(restoredActivity);
});

async function showMessagesOnly(page) {
  await page.getByRole("button", { name: "Messages-only transcript view" }).click();
}

async function activityFor(page, card) {
  await expect(card).toHaveAttribute("data-focus-activity-group", /.+/);
  const groupId = await card.getAttribute("data-focus-activity-group");
  return page.locator(`[data-focus-activity-summary="${groupId}"]`);
}

async function expectCollapsedActivity(activity) {
  await expect(activity.locator("[data-focus-activity-toggle]")).toHaveAttribute("aria-expanded", "false");
  await expect(activity.locator(".focus-activity-details")).toBeHidden();
}

async function expectExpandedActivity(activity) {
  await expect(activity.locator("[data-focus-activity-toggle]")).toHaveAttribute("aria-expanded", "true");
  await expect(activity.locator(".focus-activity-details")).toBeVisible();
  await expect(activity.locator(".focus-activity-item-text")).toContainText(tool.longCommand);

  const metrics = await activity.locator(".focus-activity-details").evaluate((details) => ({
    clientWidth: details.clientWidth,
    scrollWidth: details.scrollWidth
  }));
  expect(metrics.scrollWidth).toBeLessThanOrEqual(metrics.clientWidth + 1);
}

async function expectFullCommand(card, { wrapped = false } = {}) {
  await expect(card.locator(".compact-summary")).toHaveText(`$ ${tool.longCommand}`);
  const metrics = await card.locator(".message-details-summary").evaluate((summary) => {
    const text = summary.querySelector(".compact-summary");
    return {
      clientHeight: summary.clientHeight,
      scrollHeight: summary.scrollHeight,
      clientWidth: summary.clientWidth,
      scrollWidth: summary.scrollWidth,
      lineHeight: parseFloat(getComputedStyle(text).lineHeight)
    };
  });
  expect(metrics.scrollWidth).toBeLessThanOrEqual(metrics.clientWidth + 1);
  expect(metrics.scrollHeight).toBeLessThanOrEqual(metrics.clientHeight + 1);
  if (wrapped) expect(metrics.clientHeight).toBeGreaterThan(metrics.lineHeight * 2);
}
