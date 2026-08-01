import { expect, test } from "@playwright/test";
import { prompts, sessions, tool } from "../support/contract.mjs";
import { expectRunFinished, message, selectSession, sendPrompt } from "../support/ui.mjs";

test("show the full wrapped tool command live and after reload", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.toolSummary);
  await sendPrompt(page, prompts.longCommand);
  await expectRunFinished(page);

  const card = message(page, "assistant", "pi --no-session").last();
  await expectFullCommand(card);

  await page.setViewportSize({ width: 390, height: 844 });
  await expectFullCommand(card, { wrapped: true });

  await page.reload();
  const restoredCard = message(page, "assistant", "pi --no-session").last();
  await expectFullCommand(restoredCard, { wrapped: true });
});

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
