import { expect, test } from "@playwright/test";
import { hermes } from "../support/contract.mjs";
import { message } from "../support/ui.mjs";

test("a voice conversation starts and ends, and what was said opens, on the first taps", async ({ page }) => {
  await page.goto("/hermes");
  const call = page.getByRole("group", { name: "Voice conversation with Hermes" });
  await page.getByRole("button", { name: "Start a voice conversation" }).tap();
  await expect(page.locator("[data-voice-call-view] [data-voice-call-state]")).toHaveText("listening");
  const spoken = message(page, "user", hermes.voice.transcript).last();
  await expect(spoken).toBeAttached();

  await call.getByRole("button", { name: "end", exact: true }).tap();
  await expect(call).toBeHidden();
  await expect(page.getByLabel("Message to Hermes")).toBeVisible();

  await spoken.locator(".message-spoken-bars").tap();
  await expect(spoken.locator(".message-body")).toBeVisible();
});

test("the Hermes panel opens from the sidebar over the whole screen and closes, on the first taps", async ({ page }) => {
  expect((await page.request.post("/hermes/new")).ok()).toBe(true);
  await page.goto("/");
  await page.locator('label[aria-label="Open sessions"]').tap();
  await page.getByRole("button", { name: "Hermes", exact: true }).tap();
  const panel = page.locator("#hermes-panel");
  await expect(page.frameLocator("#hermes-panel iframe").getByLabel("Message to Hermes")).toBeVisible();
  expect(await panel.boundingBox()).toEqual({ x: 0, y: 0, ...page.viewportSize() });
  // Closing the panel goes back to the session, not to the sidebar it opened from.
  await expect(page.locator("#mobile-session-toggle")).not.toBeChecked();

  await page.frameLocator("#hermes-panel iframe").getByRole("button", { name: "Close Hermes" }).tap();
  await expect(panel).toBeHidden();

  // A reply while it is closed marks the sessions button, behind which the Hermes button is.
  const { session } = await (await page.request.get("/hermes/current")).json();
  expect((await page.request.post("/prompt", { form: { session, message: "Ping from the desk" } })).ok()).toBe(true);
  await expect(page.locator('label[aria-label="Open sessions"] .hermes-reply-dot')).toBeVisible();
});
