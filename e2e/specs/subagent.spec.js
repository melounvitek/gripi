import { expect, test } from "@playwright/test";
import { paginatedSubagent, prompts, sessions, subagents } from "../support/contract.mjs";
import { expectRunFinished, selectSession, sendPrompt } from "../support/ui.mjs";

async function subagentState(page, ids) {
  return page.locator(ids.map((id) => `article[data-tool-call-id="${id}"]`).join(",")).evaluateAll((cards) => cards.map((card) => ({
    id: card.dataset.toolCallId,
    timestamp: card.dataset.messageTimestamp,
    label: card.querySelector(".message-meta")?.textContent || ""
  })));
}

test("keep parallel subagent order and timestamps stable through reload", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.parallelSubagents);
  await sendPrompt(page, prompts.parallelSubagents);

  const first = page.locator(`article[data-tool-call-id="${subagents.firstCallId}"]`);
  const second = page.locator(`article[data-tool-call-id="${subagents.secondCallId}"]`);
  await expect(first).toContainText(subagents.firstResult);
  await expect(second).toContainText(subagents.secondProgress);
  await expect(page.getByRole("button", { name: "Abort running Pi" })).toBeVisible();
  const activeState = await subagentState(page, [subagents.firstCallId, subagents.secondCallId]);
  expect(activeState.map(({ id }) => id)).toEqual([subagents.firstCallId, subagents.secondCallId]);
  expect(activeState.every(({ timestamp, label }) => timestamp && label)).toBe(true);

  await page.reload();
  await expect(page.getByRole("heading", { level: 1, name: sessions.parallelSubagents })).toBeVisible();
  await expect(first).toHaveCount(1);
  await expect(first).toContainText(subagents.firstResult);
  await expect(second).toHaveCount(1);
  await expect(second).toContainText(subagents.secondProgress);
  expect(await subagentState(page, [subagents.firstCallId, subagents.secondCallId])).toEqual(activeState);

  await page.getByRole("button", { name: "Abort running Pi" }).click();
  await expectRunFinished(page);
  await expect(first).toHaveCount(1);
  await expect(second).toHaveCount(1);

  await page.reload();
  await expect(first).toHaveCount(1);
  await expect(first).toContainText(subagents.firstResult);
  await expect(second).toHaveCount(1);
  await expect(second).toContainText(subagents.secondResult);
});

test("replace a retained subagent when its persisted history page loads", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.paginatedSubagent);
  await sendPrompt(page, prompts.paginatedSubagent);

  const matching = page.locator(`article[data-tool-call-id="${paginatedSubagent.matchingCallId}"]`);
  const unrelated = page.locator(`article[data-tool-call-id="${paginatedSubagent.unrelatedCallId}"]`);
  await expect(matching).toHaveCount(1);
  await expect(matching).toContainText(paginatedSubagent.liveProgress);
  await expect(unrelated).toHaveCount(1);

  await page.locator("[data-conversation-history-status]").click();
  await expect(matching).toHaveCount(1);
  await expect(matching).toContainText(paginatedSubagent.persistedResult);
  await expect(unrelated).toHaveCount(1);
  const orderedIDs = await page.locator(`article[data-tool-call-id="${paginatedSubagent.matchingCallId}"], article[data-tool-call-id="${paginatedSubagent.unrelatedCallId}"]`).evaluateAll((cards) => cards.map((card) => card.dataset.toolCallId));
  expect(orderedIDs).toEqual([paginatedSubagent.matchingCallId, paginatedSubagent.unrelatedCallId]);

  await page.waitForTimeout(1400);
  await expect(matching).toHaveCount(1);
  await expect(matching).toContainText(paginatedSubagent.persistedResult);
  await page.getByRole("button", { name: "Abort running Pi" }).click();
  await expectRunFinished(page);
});
