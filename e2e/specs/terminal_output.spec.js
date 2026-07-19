import { expect, test } from "@playwright/test";
import { prompts, sessions, tool } from "../support/contract.mjs";
import { expectRunFinished, selectSession, sendPrompt } from "../support/ui.mjs";

test("renders a scrollable live and restored terminal transcript", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.terminal);
  await sendPrompt(page, prompts.terminal);

  const card = terminalCard(page);
  await expect(card).toContainText("Terminal current screen");
  await expect(card).toContainText("Terminal history 32");
  await expect(card).not.toContainText("Terminal stale screen");
  await expect(page.getByRole("button", { name: "Abort running Pi" })).toBeVisible();
  await expect(card.locator(".terminal-output-run").filter({ hasText: "Terminal current screen" })).toHaveCSS("color", "rgb(0, 205, 0)");
  await expectRunFinished(page);

  await verifyExpandedTranscript(card);

  await page.reload();
  const restoredCard = terminalCard(page);
  await expect(restoredCard).toContainText("Terminal current screen");
  await expect(restoredCard).not.toContainText("Terminal stale screen");
  await verifyExpandedTranscript(restoredCard);
});

function terminalCard(page) {
  return page.locator(".message--tool-call").filter({ hasText: `$ ${tool.terminalCommand}` }).last();
}

async function verifyExpandedTranscript(card) {
  await card.getByRole("button", { name: "Expand" }).click();
  const region = card.getByRole("region", { name: "Expanded tool output" });
  await expect(region).toContainText("Terminal history 01");
  await expect(region).toContainText("Terminal history 32");
  await expect(region).toContainText("Terminal current screen");
  await expect(region.getByText("Terminal history 20", { exact: true })).toHaveCount(1);

  const initialMetrics = await region.evaluate((element) => ({
    clientHeight: element.clientHeight,
    scrollHeight: element.scrollHeight,
    scrollTop: element.scrollTop
  }));
  expect(initialMetrics.scrollHeight).toBeGreaterThan(initialMetrics.clientHeight);
  expect(initialMetrics.scrollTop).toBeGreaterThan(0);

  await region.evaluate((element) => { element.scrollTop = 0; });
  await expect.poll(() => region.evaluate((element) => element.scrollTop)).toBe(0);
  expect(await lineIsVisible(region, "Terminal history 01")).toBe(true);

  await region.evaluate((element) => { element.scrollTop = element.scrollHeight; });
  expect(await lineIsVisible(region, "Terminal current screen")).toBe(true);
}

async function lineIsVisible(region, text) {
  return region.evaluate((element, expectedText) => {
    const line = [...element.querySelectorAll(".tool-output-line")].find((candidate) => candidate.textContent === expectedText);
    if (!line) return false;
    const regionRect = element.getBoundingClientRect();
    const lineRect = line.getBoundingClientRect();
    return lineRect.top >= regionRect.top && lineRect.bottom <= regionRect.bottom;
  }, text);
}
