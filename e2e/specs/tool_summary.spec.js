import { expect, test } from "@playwright/test";
import { prompts, sessions, tool } from "../support/contract.mjs";
import { expectRunFinished, message, selectSession, sendPrompt } from "../support/ui.mjs";

const longCommandCard = (page) => message(page, "assistant", "pi --no-session").last();

test("clamp a long tool command behind a toggle, live and after reload", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.toolSummary);
  await sendPrompt(page, prompts.longCommand);
  await expectRunFinished(page);

  await expectClampedToggleBehavior(page);

  await page.reload();
  await expect(page.getByRole("heading", { level: 1, name: sessions.toolSummary })).toBeVisible();
  await expectClampedToggleBehavior(page);
});

async function expectClampedToggleBehavior(page) {
  const card = longCommandCard(page);
  const summary = card.locator(".compact-summary");
  const showToggle = card.getByRole("button", { name: "Show" });
  await expect(showToggle).toBeVisible();
  await expect.poll(() => isClamped(summary)).toBe(true);

  await showToggle.click();
  const hideToggle = card.getByRole("button", { name: "Hide" });
  await expect(hideToggle).toBeVisible();
  await expect.poll(() => isClamped(summary)).toBe(false);
  expect(await fitsWithoutHorizontalScroll(summary)).toBe(true);

  await hideToggle.click();
  await expect(showToggle).toBeVisible();
  await expect.poll(() => isClamped(summary)).toBe(true);
}

async function isClamped(summary) {
  return summary.evaluate((element) => element.scrollHeight > element.clientHeight + 1);
}

async function fitsWithoutHorizontalScroll(summary) {
  return summary.evaluate((element) => element.scrollWidth <= element.clientWidth + 1);
}

test("toggle follows viewport width changes", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.toolSummary);
  await sendPrompt(page, prompts.wrapCommand);
  await expectRunFinished(page);

  const card = message(page, "assistant", tool.wrapCommand).last();
  await expect(card).toBeVisible();
  await expect(card.getByRole("button", { name: "Show" })).toBeHidden();

  await page.setViewportSize({ width: 390, height: 844 });
  await expect(card.getByRole("button", { name: "Show" })).toBeVisible();

  await page.setViewportSize({ width: 1440, height: 900 });
  await expect(card.getByRole("button", { name: "Show" })).toBeHidden();
});

test("keep short tool commands free of the toggle", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.toolSummary);
  await sendPrompt(page, prompts.standard);
  await expectRunFinished(page);

  const card = message(page, "assistant", tool.command).last();
  await expect(card).toBeVisible();
  await expect(card.getByRole("button", { name: "Show" })).toBeHidden();
});
