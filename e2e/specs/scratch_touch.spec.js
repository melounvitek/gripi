import { expect, test } from "@playwright/test";

test.beforeEach(async ({ page }) => {
  expect((await page.request.post("/scratch/discard", { headers: { Accept: "application/json" } })).ok()).toBe(true);
  await page.goto("/");
});

function overlay(page) {
  return page.locator("[data-scratch-overlay]");
}

function scratch(page) {
  return page.frameLocator("[data-scratch-overlay] iframe");
}

async function openScratch(page) {
  await page.locator('label[aria-label="Open sessions"]').tap();
  await page.getByRole("button", { name: "Scratch session, not saved" }).tap();
  await expect(overlay(page)).toBeVisible();
  await expect(overlay(page)).not.toHaveClass(/is-loading/);
  await expect(scratch(page).getByRole("heading", { name: /Scratch/ })).toBeVisible();
  // The sheet covers the drawer's place, which closes.
  await expect(page.locator("#mobile-session-toggle")).not.toBeChecked();
}

test("on a phone, a scratch session opens in a sheet that one tap hides or discards", async ({ page }) => {
  await openScratch(page);
  const sheet = await page.locator(".scratch-window").boundingBox();
  const viewport = page.viewportSize();
  expect(sheet.y).toBeGreaterThan(20);
  expect(Math.round(sheet.y + sheet.height)).toBe(viewport.height);

  // The strip of the page above the sheet hides it.
  await overlay(page).tap({ position: { x: viewport.width / 2, y: 10 } });
  await expect(overlay(page)).toBeHidden();

  await openScratch(page);
  await scratch(page).getByRole("button", { name: "Hide the scratch session" }).tap();
  await expect(overlay(page)).toBeHidden();

  // The keyboard leaves the composer in sight.
  await openScratch(page);
  const composer = scratch(page).getByLabel(/^Message to Pi$/);
  await composer.tap();
  await page.setViewportSize({ width: viewport.width, height: Math.round(viewport.height / 2) });
  await expect(composer).toBeInViewport();
  await page.setViewportSize(viewport);

  await scratch(page).getByRole("button", { name: "discard" }).tap();
  await expect(overlay(page)).toBeHidden();
  await page.locator('label[aria-label="Open sessions"]').tap();
  await expect(page.getByRole("button", { name: "Scratch session, not saved" })).not.toHaveClass(/is-active/);
});
