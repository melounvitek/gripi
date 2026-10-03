import { expect } from "@playwright/test";

export async function selectSession(page, title) {
  const link = page.getByRole("link", { name: new RegExp(escapeRegExp(title)) });
  // Earlier tests push seeded sessions down the sidebar. Loading more keeps it unfiltered, unlike searching.
  const loadMore = page.locator("[data-sidebar-load-more]");
  while (!await link.isVisible() && await loadMore.isVisible()) {
    await loadMore.click();
    await expect(page.locator("[data-sidebar-load-more].is-loading")).toHaveCount(0);
  }
  await expect(link).toBeVisible();
  await link.click();
  await expect(page.getByRole("heading", { level: 1, name: title })).toBeVisible();
}

export async function startSession(page, project) {
  await page.getByRole("button", { name: "New session", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "New session" });
  await dialog.getByRole("combobox", { name: "Project or path" }).fill(project);
  await dialog.getByRole("option", { name: new RegExp(escapeRegExp(project)) }).click();
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

export function activityView(page, view) {
  return page.getByRole("group", { name: "Agent activity", exact: true }).getByRole("button", { name: view, exact: true });
}

function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
