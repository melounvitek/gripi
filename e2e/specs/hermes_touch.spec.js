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

test("the Hermes panel opens over the whole screen and closes, on the first taps", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("button", { name: "Hermes", exact: true }).tap();
  const panel = page.locator("#hermes-panel");
  await expect(page.frameLocator("#hermes-panel iframe").getByLabel("Message to Hermes")).toBeVisible();
  expect(await panel.boundingBox()).toEqual({ x: 0, y: 0, ...page.viewportSize() });

  await page.frameLocator("#hermes-panel iframe").getByRole("button", { name: "Close Hermes" }).tap();
  await expect(panel).toBeHidden();
});
