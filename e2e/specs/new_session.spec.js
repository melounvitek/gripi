import { expect, test } from "@playwright/test";
import { prompts, replies, sessions } from "../support/contract.mjs";
import { expectRunFinished, message, selectSession, sendPrompt } from "../support/ui.mjs";

test("delete a pending session before its first assistant response", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("button", { name: "New session", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "New session" });
  await dialog.getByRole("combobox", { name: "Project" }).click();
  await page.getByRole("option", { name: /new-session-desktop/ }).click();
  await dialog.getByRole("button", { name: "Start session" }).click();
  const title = "New session (pending first assistant response)";
  await expect(page.getByRole("heading", { level: 1, name: title })).toBeVisible();
  const path = await page.locator('.session-row[data-current="true"]').getAttribute("data-session-path");
  const selector = await page.evaluate((path) => `.session-row[data-session-path="${CSS.escape(path)}"]`, path);
  const row = page.locator(selector);
  await row.getByRole("button", { name: `Pin session ${title}`, exact: true }).click();
  await expect(row).toHaveAttribute("data-pinned", "true");

  await selectSession(page, sessions.history);
  const clearFilters = page.getByRole("link", { name: "Clear filters", exact: true });
  if (await clearFilters.isVisible()) await clearFilters.click();
  await row.getByRole("button", { name: `Session actions for ${title}` }).click();
  await page.getByRole("menuitem", { name: "Delete session…" }).click();
  const deleteDialog = page.getByRole("dialog", { name: "Delete session" });
  await expect(deleteDialog).toContainText(title);
  const deleted = page.waitForResponse("**/sessions/delete");
  await deleteDialog.getByRole("button", { name: "Delete session", exact: true }).click();
  expect((await deleted).status()).toBe(200);
  await expect(deleteDialog).toBeHidden();
  await expect(row).toHaveCount(0);
  await page.reload();
  await expect(row).toHaveCount(0);
  await expect(page.getByRole("heading", { level: 1, name: sessions.history })).toBeVisible();
});

test("start a session in a configured directory and persist its first response", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("button", { name: "New session" }).click();

  const dialog = page.getByRole("dialog", { name: "New session" });
  await expect(dialog).toBeVisible();
  await dialog.getByRole("combobox", { name: "Project" }).click();
  await page.getByRole("option", { name: /new-session-desktop/ }).click();
  await dialog.getByRole("button", { name: "Start session" }).click();
  await expect(page.getByRole("heading", { level: 1, name: "New session (pending first assistant response)" })).toBeVisible();
  await expect(page.getByRole("heading", { level: 2, name: "Current session" })).toBeHidden();

  let session = page.getByRole("link", { name: /New session \(pending first assistant response\)/ });
  let row = page.locator(".session-row").filter({ has: session });
  await row.getByRole("button", { name: /Pin session New session/ }).click();
  await expect(row).toHaveAttribute("data-pinned", "true");
  await expect(page.getByRole("heading", { level: 2, name: "Pinned" })).toBeVisible();

  const composer = page.getByLabel("Message to Pi");
  await composer.fill("Follow the AGENTS file from ../");
  const sibling = page.getByRole("option", { name: "../new-session-mobile/", exact: true });
  await expect(sibling).toBeVisible();
  await sibling.click();
  await expect(composer).toHaveValue("Follow the AGENTS file from ../new-session-mobile/");

  await sendPrompt(page, prompts.newSession);
  await expect(message(page, "assistant", replies.newSession)).toBeVisible();
  await expectRunFinished(page);

  await page.reload();
  await expect(page.getByRole("heading", { level: 1, name: prompts.newSession })).toBeVisible();
  session = page.getByRole("link", { name: new RegExp(prompts.newSession) });
  row = page.locator(".session-row").filter({ has: session });
  await expect(row).toHaveAttribute("data-pinned", "true");
  await expect(message(page, "user", prompts.newSession)).toBeVisible();
  await expect(message(page, "assistant", replies.newSession)).toBeVisible();
});
