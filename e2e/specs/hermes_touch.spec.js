import { expect, test } from "@playwright/test";
import { hermes } from "../support/contract.mjs";
import { expectRunFinished, sendPrompt } from "../support/ui.mjs";

test("a new Hermes session starts from the dialog on the first tap", async ({ page }) => {
  await page.goto("/");
  await page.locator('label[aria-label="Open sessions"]').tap();
  await page.getByRole("button", { name: "New session", exact: true }).tap();
  const dialog = page.getByRole("dialog", { name: "New session", exact: true });
  await expect(dialog.locator("[data-new-session-project]").first()).toBeVisible();

  await dialog.getByRole("option", { name: /hermes/ }).tap();
  await expect(page.getByRole("heading", { level: 1, name: "New session (pending first assistant response)" })).toBeVisible();
  await expect(page.locator(".session-header-agent")).toHaveText("hermes");

  await sendPrompt(page, "Say hello from a phone");
  await expect(page.locator('#live-output article[data-role="assistant"]').filter({ hasText: `${hermes.run.replyPrefix}Say hello from a phone` })).toBeVisible();
  await expectRunFinished(page);
});

test("a voice conversation starts and ends on the first taps", async ({ page }) => {
  await page.goto(`/?session=${encodeURIComponent(`hermes:${hermes.saved.id}`)}`);
  const call = page.getByRole("group", { name: "Voice conversation with Hermes" });
  await page.getByRole("button", { name: "Start a voice conversation" }).tap();
  await expect(call.locator("[data-voice-call-state]")).toHaveText("listening");

  await call.getByRole("button", { name: "end", exact: true }).tap();
  await expect(call).toBeHidden();
  await expect(page.getByLabel("Message to Hermes")).toBeVisible();
});

test("the agent filter works on the first taps", async ({ page }) => {
  await page.goto("/");
  await page.locator('label[aria-label="Open sessions"]').tap();
  await page.getByRole("combobox", { name: "Filter sessions by agent" }).tap();
  await page.getByRole("option", { name: "hermes" }).tap();
  await expect(page.locator(".session-row").filter({ hasText: hermes.saved.title })).toBeVisible();
  await expect(page.locator('.session-row[data-agent="pi"]')).toHaveCount(0);
});
