import { expect, test } from "@playwright/test";
import { sessions } from "../support/contract.mjs";
import { message } from "../support/ui.mjs";

test("switch focus between the composer and conversation in a narrow desktop window", async ({ page }) => {
  await page.goto("/");

  await page.getByRole("button", { name: "Search sessions" }).click();
  await page.getByRole("searchbox", { name: "Search sessions" }).fill("History Desktop");
  await page.getByRole("link", { name: new RegExp(sessions.history) }).click();
  await expect(page.getByRole("heading", { level: 1, name: sessions.history })).toBeVisible();
  await page.setViewportSize({ width: 600, height: 900 });

  const composer = page.locator('textarea[name="message"]');
  const conversation = page.locator("#conversation-scroll");
  await expect(composer).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(conversation).toBeFocused();

  await page.keyboard.press("Tab");
  await expect(composer).toBeFocused();
});

test("find, select, and pin a session with persisted history", async ({ page }) => {
  await page.goto("/");

  await page.getByRole("button", { name: "Search sessions" }).click();
  await page.getByRole("searchbox", { name: "Search sessions" }).fill("History Desktop");
  let session = page.getByRole("link", { name: new RegExp(sessions.history) });
  await expect(session).toBeVisible();
  await session.click();

  await expect(page.getByRole("heading", { level: 1, name: sessions.history })).toBeVisible();
  await expect(page.getByRole("link", { name: new RegExp(sessions.history) })).toHaveAttribute("aria-current", "page");
  await expect(message(page, "user", "Persisted browser question")).toBeVisible();
  await expect(message(page, "assistant", "Persisted browser answer")).toBeVisible();

  session = page.getByRole("link", { name: new RegExp(sessions.history) });
  const row = page.locator(".session-row").filter({ has: session });
  await row.getByRole("button", { name: "Pin session" }).click();
  await expect(row.getByRole("button", { name: "Unpin session" })).toBeVisible();
  await expect(page.getByRole("heading", { level: 2, name: "Pinned" })).toBeVisible();
});
