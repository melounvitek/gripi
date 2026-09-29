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

    const shells = page.locator('article[data-role="bashExecution"]');
    const shellCount = await shells.count();
    await page.getByLabel("Message to Pi").fill(`!!${nativeBash.excluded.command}`);
    await page.locator(".prompt-form").evaluate((form) => form.requestSubmit());
    // Earlier tests leave the same shell card in this shared session.
    await expect(shells).toHaveCount(shellCount + 1);
    const shell = shells.last();
    await expect(shell).toContainText(nativeBash.excluded.output.trim());
    await expectRunFinished(page);
    await expectAlignedCard(shell, assistant);

    await page.getByLabel("Message to Pi").fill("/compact");
    await page.locator(".prompt-form").evaluate((form) => form.requestSubmit());
    const pending = page.locator('[data-pending-compaction="true"]');
    await expect(pending).toBeVisible();
    // compaction_start replaces the optimistic card; measure the replacement.
    await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "running");
    await expectAlignedCard(pending, assistant);
    await expectRunFinished(page);

    const compacted = page.locator(".message--compaction").last();
    await expectAlignedCard(compacted, assistant);
    await expectCompactionToggle(compacted, width);

    await reloadWithoutLivePi(page);
    await expectAlignedCard(toolCard, assistant);
    await expectAlignedCard(shell, assistant);
    await expectAlignedCard(compacted, assistant);
    await expectCompactionToggle(compacted, width);
    await compacted.scrollIntoViewIfNeeded();
    await page.screenshot({ path: testInfo.outputPath("message-spacing.png") });
  });
}

for (const touch of [false, true]) {
  test.describe(touch ? "on touch" : "with a fine pointer", () => {
    test.use({ hasTouch: touch });

    test(`share one text edge and show times on user messages and answers ${touch ? "on touch" : "with a fine pointer"}`, async ({ page }) => {
      await page.goto("/");
      await selectSession(page, sessions.compactionFollowUp);
      await sendPrompt(page, prompts.standard);
      await expectRunFinished(page);
      await expectMessageTimes(page, touch);

      await reloadWithoutLivePi(page);
      await expectMessageTimes(page, touch);
    });
  });
}

// The managed E2E gateway retires idle Pi after 2s, and a page rendered while Pi ran then re-renders the whole
// transcript. Waiting for that after reloading keeps it from replacing elements while they are measured.
async function reloadWithoutLivePi(page) {
  await page.reload();
  if (process.env.GRIPI_E2E_FAKE_PI_LOG) await expect(page.locator("#live-output")).toHaveAttribute("data-session-sync-mode", "available");
}

async function expectMessageTimes(page, touch) {
  const user = message(page, "user", prompts.standard).last();
  const answer = message(page, "assistant", replies.standard).last();
  const toolCard = page.locator(".message--tool-call").filter({ hasText: tool.command }).last();
  await expect(answer).toHaveAttribute("data-final-assistant-response", "true");
  const edge = await textGeometry(answer.locator(".message-body"));
  for (const text of [user.locator(".message-body"), toolCard.locator(".compact-summary")]) {
    expect(Math.abs((await textGeometry(text)).left - edge.left)).toBeLessThanOrEqual(1);
  }

  for (const card of [user, answer]) {
    const meta = card.locator(".message-meta");
    await expect(meta).toBeVisible();
    await expect(meta).toHaveText(/^(?:[A-Z][a-z]{2} \d{1,2} )?\d\d:\d\d$/);
    await expect(meta).toHaveAttribute("title", /^\d{4}-\d\d-\d\d \d\d:\d\d$/);
    const time = await textGeometry(meta);
    expect(Math.abs(time.left - edge.left)).toBeLessThanOrEqual(1);
    const body = await card.locator(".message-body").boundingBox();
    expect(time.baseline).toBeGreaterThan(body.y + body.height);
  }
  const answerTime = await answer.locator(".message-meta").boundingBox();
  const copy = await answer.getByRole("button", { name: "Copy" }).boundingBox();
  expect(copy.x).toBeGreaterThan(answerTime.x + answerTime.width);
  expect(Math.abs((copy.y + copy.height / 2) - (answerTime.y + answerTime.height / 2))).toBeLessThanOrEqual(1);

  // Other rows keep their time in the DOM, revealed only by desktop hover at the end of the first line.
  const toolTime = toolCard.locator(".message-meta");
  await expect(toolTime).toHaveText(/\d\d:\d\d$/);
  await expect(toolTime).toBeHidden();
  if (touch) {
    expect(await toolTime.evaluate((element) => element.getClientRects().length)).toBe(0);
    return;
  }
  await toolCard.hover();
  await expect(toolTime).toBeVisible();
  const [summary, time] = [await textGeometry(toolCard.locator(".compact-summary")), await textGeometry(toolTime)];
  expect(Math.abs(time.baseline - summary.baseline)).toBeLessThanOrEqual(1);
  const card = await toolCard.evaluate((element) => ({ right: element.getBoundingClientRect().right, padding: Number.parseFloat(getComputedStyle(element).paddingRight) }));
  expect(Math.abs(time.right - (card.right - card.padding))).toBeLessThanOrEqual(1);
  await page.mouse.move(0, 0);
  await expect(toolTime).toBeHidden();
}

// Left edge, right edge and baseline of an element's first line of text.
async function textGeometry(locator) {
  return locator.evaluate((element) => {
    const walker = element.ownerDocument.createTreeWalker(element, NodeFilter.SHOW_TEXT, { acceptNode: (node) => node.textContent.trim() ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_SKIP });
    const text = walker.nextNode();
    const range = element.ownerDocument.createRange();
    range.selectNodeContents(text);
    const line = range.getClientRects()[0];
    const probe = element.ownerDocument.createElement("span");
    probe.style.cssText = "display: inline-block; width: 0; height: 0; vertical-align: baseline";
    text.before(probe);
    const baseline = probe.getBoundingClientRect().top;
    probe.remove();
    return { left: line.left, right: line.right, baseline };
  });
}

async function expectAlignedCard(card, assistant) {
  await expect(card).toBeVisible();
  const reference = await assistant.boundingBox();
  const bounds = await card.boundingBox();
  expect(Math.abs(bounds.x - reference.x)).toBeLessThanOrEqual(1);
  expect(Math.abs(bounds.width - reference.width)).toBeLessThanOrEqual(1);
  const alignment = await card.evaluate((element) => {
    const left = element.getBoundingClientRect().left + Number.parseFloat(getComputedStyle(element).paddingLeft);
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
