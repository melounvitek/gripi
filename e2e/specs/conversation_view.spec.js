import { expect, test } from "@playwright/test";
import { prompts, sessions, tool } from "../support/contract.mjs";
import { activityView, expectRunFinished, message, selectSession, sendPrompt } from "../support/ui.mjs";

test("shows one tool result after reloading an active command", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.toolReload);
  await sendPrompt(page, prompts.longCommand);
  await expect(message(page, "assistant", tool.longCommand)).toBeVisible();

  await page.reload();

  await expect(page.locator("article[data-tool-call-id]").filter({ hasText: tool.result })).toHaveCount(1);
  await expectRunFinished(page);
});

test("switches agent activity with accessible Brief and Full segments", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.toolSummary);
  const toolCalls = message(page, "assistant", `$ ${tool.longCommand}`);
  const previousCount = await toolCalls.count();
  await sendPrompt(page, prompts.longCommand);

  const toolCall = toolCalls.nth(previousCount);
  await expect(toolCall).toBeVisible();

  const brief = activityView(page, "Brief");
  const full = activityView(page, "Full");
  await expect(brief).toHaveAttribute("aria-pressed", "false");
  await expect(full).toHaveAttribute("aria-pressed", "true");
  await brief.click();

  await expect(page.locator(".conversation-panel")).toHaveClass(/is-conversation-focused/);
  await expect(brief).toHaveAttribute("aria-pressed", "true");
  await expect(full).toHaveAttribute("aria-pressed", "false");
  await expect(toolCall).toBeHidden();

  // The selected segment stays selected instead of toggling back.
  await brief.click();
  await expect(brief).toHaveAttribute("aria-pressed", "true");
  await expect(toolCall).toBeHidden();

  await full.click();
  await expect(page.locator(".conversation-panel")).not.toHaveClass(/is-conversation-focused/);
  await expect(full).toHaveAttribute("aria-pressed", "true");
  await expect(toolCall).toBeVisible();
  await expectRunFinished(page);

  await page.reload();
  await expect(full).toHaveAttribute("aria-pressed", "true");
  await expect(toolCall).toBeVisible();
  await brief.focus();
  await page.keyboard.press("Space");
  await expect(brief).toHaveAttribute("aria-pressed", "true");
  await expect(toolCall).toBeHidden();
  await page.keyboard.press("Tab");
  await expect(full).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(full).toHaveAttribute("aria-pressed", "true");
  await expect(toolCall).toBeVisible();

  await page.keyboard.press("Control+f");
  const find = page.getByRole("searchbox", { name: "Find in conversation" });
  await find.fill("deterministic-tool-result");
  const count = page.locator("[data-current-session-find-count]");
  await expect(count).toHaveText("1 / 1");

  await brief.click();
  await expect(count).toHaveText("0 / 0");
  await full.click();
  await expect(count).toHaveText("1 / 1");
});

test("remembers the Brief view after a reload", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.toolSummary);
  const brief = activityView(page, "Brief");
  await brief.click();
  await expect(brief).toHaveAttribute("aria-pressed", "true");

  await page.reload();

  await expect(brief).toHaveAttribute("aria-pressed", "true");
  await expect(activityView(page, "Full")).toHaveAttribute("aria-pressed", "false");
  await expect(page.locator(".conversation-panel")).toHaveClass(/is-conversation-focused/);
});

test("opens a long conversation at the latest message before the app script loads", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.paginatedSubagent);
  await page.route(/\/assets\/app\.js/, (route) => route.abort());

  await page.reload();

  const distances = await page.locator("#conversation-scroll").evaluate((element) => ({
    overflow: element.scrollHeight - element.clientHeight,
    fromBottom: element.scrollHeight - element.scrollTop - element.clientHeight
  }));
  expect(distances.overflow).toBeGreaterThan(0);
  expect(distances.fromBottom).toBeLessThan(5);
});
