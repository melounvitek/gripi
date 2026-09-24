import { expect, test } from "@playwright/test";
import { nativeBash, prompts, replies, sessions, tool } from "../support/contract.mjs";
import { expectRunFinished, message, selectSession, sendPrompt } from "../support/ui.mjs";

test.use({ hasTouch: true });

for (const width of [1440, 390, 320]) {
  test(`align compact cards live and after reload at ${width}px`, async ({ page }, testInfo) => {
    await page.goto("/");
    await selectSession(page, sessions.compactionFollowUp);
    await page.setViewportSize({ width, height: 900 });
    await sendPrompt(page, prompts.standard);
    await expectRunFinished(page);
    const assistant = message(page, "assistant", replies.standard).last();
    const toolCard = page.locator(".message--tool-call").filter({ hasText: tool.command }).last();
    await expectAlignedCard(toolCard, assistant);

    await page.getByLabel("Message to Pi").fill(`!!${nativeBash.excluded.command}`);
    await page.locator(".prompt-form").evaluate((form) => form.requestSubmit());
    const shell = page.locator('article[data-role="bashExecution"]').last();
    await expect(shell).toContainText(nativeBash.excluded.output.trim());
    await expectRunFinished(page);
    await expectAlignedCard(shell, assistant);

    await page.getByLabel("Message to Pi").fill("/compact");
    await page.locator(".prompt-form").evaluate((form) => form.requestSubmit());
    const pending = page.locator('[data-pending-compaction="true"]');
    await expect(pending).toBeVisible();
    await expectAlignedCard(pending, assistant);
    await expectRunFinished(page);

    const compacted = page.locator(".message--compaction").last();
    await expectAlignedCard(compacted, assistant);
    await expectCompactionToggle(compacted, width);

    await page.reload();
    await expectAlignedCard(toolCard, assistant);
    await expectAlignedCard(shell, assistant);
    await expectAlignedCard(compacted, assistant);
    await expectCompactionToggle(compacted, width);
    await compacted.scrollIntoViewIfNeeded();
    await page.screenshot({ path: testInfo.outputPath("message-spacing.png") });
  });
}

async function expectAlignedCard(card, assistant) {
  await expect(card).toBeVisible();
  const reference = await assistant.boundingBox();
  const bounds = await card.boundingBox();
  expect(Math.abs(bounds.x - reference.x)).toBeLessThanOrEqual(1);
  expect(Math.abs(bounds.width - reference.width)).toBeLessThanOrEqual(1);
  const alignment = await card.evaluate((element) => {
    const left = element.querySelector(".message-header").getBoundingClientRect().left;
    const contents = element.querySelectorAll(".compact-summary, .bash-execution-status, .message-body");
    return Array.from(contents).filter((node) => node.getClientRects().length).map((node) => ({
      offset: node.getBoundingClientRect().left - left,
      overflow: node.scrollWidth - node.clientWidth
    }));
  });
  for (const { offset, overflow } of alignment) {
    expect(Math.abs(offset)).toBeLessThanOrEqual(1);
    expect(overflow).toBeLessThanOrEqual(1);
  }
}

async function expectCompactionToggle(card, width) {
  const details = card.locator("details");
  const summary = card.locator("summary");
  const action = card.locator(".compaction-details-action");
  await expect(details).not.toHaveAttribute("open");
  await expect(action).toBeVisible();
  const titleBounds = await card.locator(".compact-summary").boundingBox();
  const actionBounds = await action.boundingBox();
  const summaryBounds = await summary.boundingBox();
  expect(actionBounds.x + actionBounds.width).toBeLessThanOrEqual(summaryBounds.x + summaryBounds.width + 1);
  if (width >= 390) {
    expect(actionBounds.x - (titleBounds.x + titleBounds.width)).toBeGreaterThan(0);
    expect(actionBounds.x - (titleBounds.x + titleBounds.width)).toBeLessThanOrEqual(16);
  } else {
    expect(Math.abs(actionBounds.x - titleBounds.x)).toBeLessThanOrEqual(1);
    expect(actionBounds.y).toBeGreaterThanOrEqual(titleBounds.y + titleBounds.height);
  }
  await summary.tap();
  await expect(details).toHaveAttribute("open", "");
  await expect(card.locator(".message-body")).toHaveText("Fixture compaction");
  await expect(card.locator(".message-body")).toBeVisible();
  await summary.tap();
  await expect(details).not.toHaveAttribute("open");
  await expect(card.locator(".message-body")).toBeHidden();
}
