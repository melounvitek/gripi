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
  const expandToggle = card.getByRole("button", { name: "Expand" });
  await expect(expandToggle).toBeVisible();
  await expect.poll(() => isClamped(summary)).toBe(true);

  await expandToggle.click();
  const collapseToggle = card.getByRole("button", { name: "Collapse" });
  await expect(collapseToggle).toBeVisible();
  await expect.poll(() => isClamped(summary)).toBe(false);
  expect(await fitsWithoutHorizontalScroll(summary)).toBe(true);

  await collapseToggle.click();
  await expect(expandToggle).toBeVisible();
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
  await expect(card.getByRole("button", { name: "Expand" })).toBeHidden();

  await page.setViewportSize({ width: 390, height: 844 });
  await expect(card.getByRole("button", { name: "Expand" })).toBeVisible();

  await page.setViewportSize({ width: 1440, height: 900 });
  await expect(card.getByRole("button", { name: "Expand" })).toBeHidden();

  const borderlineWidth = await card.evaluate((article) => {
    const summary = article.querySelector(".compact-summary");
    const toggle = article.querySelector("[data-tool-summary-toggle]");
    for (let width = 320; width <= 900; width += 4) {
      article.style.width = `${width}px`;
      toggle.hidden = true;
      const fitsWithoutToggle = summary.scrollHeight <= summary.clientHeight + 1;
      toggle.hidden = false;
      const clampsWithToggle = summary.scrollHeight > summary.clientHeight + 1;
      if (fitsWithoutToggle && clampsWithToggle) return width;
    }
    return null;
  });
  expect(borderlineWidth).not.toBeNull();

  await card.evaluate((article) => {
    article.style.width = "320px";
    window.dispatchEvent(new Event("resize"));
  });
  await expect(card.getByRole("button", { name: "Expand" })).toBeVisible();

  await card.evaluate((article, width) => {
    article.style.width = `${width}px`;
    window.dispatchEvent(new Event("resize"));
  }, borderlineWidth);
  await expect(card.getByRole("button", { name: "Expand" })).toBeHidden();
});

test("conversation find reveals a clamped tail match and restores the summary", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.toolSummary);
  await sendPrompt(page, prompts.longCommand);
  await expectRunFinished(page);

  const card = message(page, "assistant", "pi --no-session").last();
  await expect(card.getByRole("button", { name: "Expand" })).toBeVisible();
  await page.keyboard.press("Control+f");
  const find = page.getByRole("searchbox", { name: "Find in conversation" });
  await expect(find).toBeVisible();
  await find.fill("deterministic-review-tail-marker");
  const activeMatch = page.locator("mark.current-session-find-match.is-active");
  await expect(activeMatch).toHaveText("deterministic-review-tail-marker");
  const matchedCard = page.locator("article.message--tool-call").filter({ has: activeMatch });
  await expect(matchedCard.getByRole("button", { name: "Collapse" })).toBeVisible();
  const fingerprint = await matchedCard.getAttribute("data-message-fingerprint");
  const restoredCard = page.locator(`article[data-message-fingerprint=${JSON.stringify(fingerprint)}]`);

  await page.getByRole("button", { name: "Close find" }).click();
  await expect(restoredCard.getByRole("button", { name: "Expand" })).toBeVisible();
  await expect.poll(() => isClamped(restoredCard.locator(".compact-summary"))).toBe(true);

  await sendPrompt(page, prompts.standard);
  await expectRunFinished(page);
  const shortCard = message(page, "assistant", tool.command).last();
  await page.keyboard.press("Control+f");
  await find.fill("tool-command-ran");
  await expect(shortCard.locator("mark.current-session-find-match.is-active")).toHaveText("tool-command-ran");
  await expect(shortCard.locator("[data-tool-summary-toggle]")).toBeHidden();
});

test("remeasure a live tool summary when returning from messages-only view", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.toolSummary);
  const view = page.locator("[data-conversation-view-select]");
  await view.selectOption("conversation");

  await sendPrompt(page, prompts.longCommand);
  await expectRunFinished(page);
  const card = message(page, "assistant", "pi --no-session").last();
  await expect(card).toBeHidden();

  await view.selectOption("full");
  await expect(card).toBeVisible();
  await expect(card.getByRole("button", { name: "Expand" })).toBeVisible();
});

test("keep short tool commands free of the toggle", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.toolSummary);
  await sendPrompt(page, prompts.standard);
  await expectRunFinished(page);

  const card = message(page, "assistant", tool.command).last();
  await expect(card).toBeVisible();
  await expect(card.getByRole("button", { name: "Expand" })).toBeHidden();
});
