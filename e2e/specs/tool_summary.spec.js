import { expect, test } from "@playwright/test";
import { prompts, sessions, tool } from "../support/contract.mjs";
import { expectRunFinished, message, selectSession, sendPrompt } from "../support/ui.mjs";

test.use({ hasTouch: true });

test("show the full wrapped tool command live and after reload", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.toolSummary);
  // Reproduce the matching history left by conversation_view.spec.js, even in isolation.
  await sendPrompt(page, prompts.longCommand);
  await expectRunFinished(page);
  const delivery = await controlToolEvents(page);
  await page.reload();
  const cards = message(page, "assistant", "pi --no-session");
  const previousCount = await cards.count();
  expect(previousCount).toBeGreaterThan(0);

  await sendPrompt(page, prompts.longCommand);
  const card = cards.nth(previousCount);
  // History must not satisfy readiness while the current run's events are withheld.
  await expect(card).toHaveCount(0);
  delivery.phase = "command";
  await expectFullCommand(card);

  await page.setViewportSize({ width: 390, height: 844 });
  await expectFullCommand(card, { wrapped: true });
  await showMessagesOnly(page);
  const activity = await activityFor(page, card);
  await expectCollapsedActivity(activity);
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "running");
  const toggle = activity.locator("[data-focus-activity-toggle]");
  await toggle.tap({ trial: true });
  // Keep the target visible but slightly above the follow-live destination.
  const conversation = page.locator("#conversation-scroll");
  await conversation.evaluate((element) => {
    element.scrollTop = element.scrollHeight - element.clientHeight - 40;
  });
  const box = await toggle.boundingBox();
  const touch = await page.context().newCDPSession(page);
  await touch.send("Input.dispatchTouchEvent", {
    type: "touchStart",
    touchPoints: [{ x: box.x + box.width / 2, y: box.y + box.height / 2 }]
  });
  // Deliver real tool output between touch-down and touch-up, not after completion.
  delivery.phase = "output";
  await expect(card).toContainText(tool.result);
  // Let scheduled layout/scroll work run while the finger is still down.
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  await touch.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
  await touch.detach();
  await expectExpandedActivity(activity);
  delivery.phase = "complete";
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

// Keep real gateway events ordered, but control their delivery to the live renderer.
// Advancing the server cursor is safe because withheld events remain in this queue.
async function controlToolEvents(page) {
  const delivery = { phase: "held" };
  const pending = [];
  await page.route(/\/events(?:\?|$)/, async (route) => {
    const response = await route.fetch();
    const payload = await response.json();
    pending.push(...payload.events);
    const events = [];
    while (pending.length > 0 && delivery.phase !== "held") {
      const next = pending[0];
      if (delivery.phase === "command" && next.type === "tool_execution_start") break;
      if (delivery.phase === "output" && next.type === "tool_execution_end") break;
      events.push(pending.shift());
    }
    await route.fulfill({ response, json: { ...payload, events } });
  });
  return delivery;
}

async function showMessagesOnly(page) {
  await page.getByRole("switch", { name: "Show agent activity" }).click();
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
