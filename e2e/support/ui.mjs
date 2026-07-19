import { expect } from "@playwright/test";

export async function selectSession(page, title) {
  const link = page.getByRole("link", { name: new RegExp(escapeRegExp(title)) });
  await expect(link).toBeVisible();
  await link.click();
  await expect(page.getByRole("heading", { level: 1, name: title })).toBeVisible();
}

export function message(page, role, text) {
  return page.locator(`article[data-role="${role}"]`).filter({ hasText: text });
}

export async function sendPrompt(page, text) {
  const composer = page.getByLabel("Message to Pi");
  await composer.fill(text);
  const sendButton = page.getByRole("button", { name: /Send$/ });
  if (await sendButton.isVisible()) await sendButton.click();
  else await composer.press("Enter");
}

export async function expectRunFinished(page) {
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "done");
  await expect(page.getByLabel("Message to Pi")).toBeEnabled();
  await expect(page.getByRole("button", { name: "Abort running Pi" })).toBeHidden();
}

function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
