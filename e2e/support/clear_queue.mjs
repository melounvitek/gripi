import { expect } from "@playwright/test";
import { prompts } from "./contract.mjs";
import { message, selectSession } from "./ui.mjs";

export const clearQueueConfirmation = "Remove all queued messages? Pi will keep running.";
export const queuedSteering = "Discard this queued steering message";
export const queuedFollowUp = "Discard this queued follow-up message";
export const clearQueueDraft = "Keep this unsent draft";

export async function prepareClearQueue(page, title, mobile = false) {
  await page.goto("/?show_all_sessions=1");
  if (mobile) {
    await page.locator('label[aria-label="Open sessions"]').tap();
    await page.getByRole("link", { name: title, exact: false }).tap();
    await expect(page.getByRole("heading", { level: 1, name: title })).toBeVisible();
  } else {
    await selectSession(page, title);
  }
  const send = async (text) => {
    await page.getByLabel("Message to Pi").fill(text);
    if (mobile) await page.locator(".send-button").tap();
    else await page.getByLabel("Message to Pi").press("Enter");
  };
  await expect(page.locator("[data-clear-queue]")).toBeHidden();
  await send(prompts.clearQueueStart);
  await expectClearQueueRunning(page);
  await expect(page.locator("[data-clear-queue]")).toBeHidden();
  await send(queuedSteering);
  await page.getByRole("button", { name: "More send options" }).click();
  await page.getByRole("button", { name: "Queue follow-up" }).click();
  await send(queuedFollowUp);
  await expectPendingQueue(page);
}

export async function attachClearQueueDraft(page) {
  await page.getByLabel("Message to Pi").fill(clearQueueDraft);
  const image = await page.screenshot();
  await page.locator("#image-input").setInputFiles({
    name: "keep-draft.png", mimeType: "image/png", buffer: image
  });
  await expectClearQueueDraft(page);
  return image;
}

export async function expectClearQueueDraft(page) {
  await expect(page.getByLabel("Message to Pi")).toHaveValue(clearQueueDraft);
  await expect(page.locator(".attachment-tray img")).toHaveCount(1);
  await expect(page.locator(".attachment-tray img")).toBeVisible();
  await expect(page.locator(".attachment-tray")).toContainText("keep-draft.png");
}

export async function expectPendingQueue(page) {
  await expect(page.locator(".pending-message--steering")).toHaveText(`Steering: ${queuedSteering}`);
  await expect(page.locator(".pending-message--follow-up")).toHaveText(`Follow-up: ${queuedFollowUp}`);
  await expect(page.getByRole("button", { name: "Clear queue", exact: true })).toBeVisible();
}

export async function expectClearQueueRunning(page) {
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "running");
  await expect(page.getByRole("button", { name: "Abort running Pi" })).toBeVisible();
  await expect(page.getByLabel("Message to Pi")).toBeEnabled();
}

export async function expectClearedQueue(page) {
  await expect(page.locator(".pending-message")).toHaveCount(0);
  await expect(page.locator("[data-clear-queue]")).toBeHidden();
  await expect(message(page, "user", queuedSteering)).toHaveCount(0);
  await expect(message(page, "user", queuedFollowUp)).toHaveCount(0);
  await expectClearQueueRunning(page);
}

export async function activateClearQueue(page, { accept = true, mobile = false } = {}) {
  const dialogPromise = page.waitForEvent("dialog");
  const button = page.getByRole("button", { name: "Clear queue", exact: true });
  const activation = mobile ? button.tap() : button.click();
  const dialog = await dialogPromise;
  const type = dialog.type();
  const text = dialog.message();
  if (accept) await dialog.accept();
  else await dialog.dismiss();
  await activation;
  expect(type).toBe("confirm");
  expect(text).toBe(clearQueueConfirmation);
}

export async function stopClearQueueRun(page) {
  const session = await page.locator('.prompt-form [name="session"]').inputValue().catch(() => "");
  if (session) await page.request.post("/abort", { form: { session }, headers: { Accept: "application/json" } });
}
