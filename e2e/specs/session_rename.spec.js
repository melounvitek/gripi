import { expect, test } from "@playwright/test";

test("session rename shows saving feedback and supports retry on the first tap", async ({ page, isMobile }, testInfo) => {
  const activate = (locator) => isMobile ? locator.tap() : locator.click();
  await page.goto("/");
  if (isMobile) await activate(page.locator('label[aria-label="Open sessions"]'));

  const row = page.locator('.session-row[data-current="true"]');
  const sessionPath = await row.getAttribute("data-session-path");
  const originalName = await row.getAttribute("data-session-name");
  const renamedName = "Renamed <session> & notes";
  const errorMessage = "Session could not be renamed. Try again.";
  await activate(row.getByRole("button", { name: `Session actions for ${originalName}` }));
  await activate(page.getByRole("menuitem", { name: "Rename…" }));
  const dialog = page.getByRole("dialog", { name: "Rename session" });
  const nameInput = dialog.getByRole("textbox", { name: "Name" });
  await nameInput.fill(`  ${renamedName}  `);

  let releaseRename;
  const renameRelease = new Promise((resolve) => { releaseRename = resolve; });
  await page.route("**/sessions/rename", async (route) => {
    await renameRelease;
    await route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: errorMessage }) });
  });
  try {
    await activate(dialog.getByRole("button", { name: "Rename", exact: true }));
    await expect(dialog.getByRole("button", { name: "Renaming…" })).toBeDisabled();
    await expect(nameInput).toBeDisabled();
    await expect(dialog.getByRole("button", { name: "Cancel" })).toBeDisabled();
    await expect(dialog).toBeVisible();
    await expect(row.locator(".session-title")).toHaveText(originalName);
    await page.screenshot({ path: testInfo.outputPath("renaming.png") });
  } finally {
    releaseRename();
    await page.unrouteAll({ behavior: "wait" });
  }

  await expect(dialog.getByRole("alert")).toHaveText(errorMessage);
  await expect(dialog.getByRole("button", { name: "Rename", exact: true })).toBeEnabled();
  await expect(nameInput).toBeEnabled();
  await expect(nameInput).toHaveValue(`  ${renamedName}  `);
  await expect(row.locator(".session-title")).toHaveText(originalName);

  let releaseSidebar;
  const sidebarRelease = new Promise((resolve) => { releaseSidebar = resolve; });
  await page.route(/\/sidebar(?:\?|$)/, async (route) => {
    await sidebarRelease;
    await route.abort("connectionfailed");
  });
  try {
    await activate(dialog.getByRole("button", { name: "Rename", exact: true }));
    await expect(dialog).toBeHidden();
    await expect(row.locator(".session-title")).toHaveText(renamedName);
    await expect(page.locator(".session-header-name")).toHaveText(renamedName);
    await expect(page).toHaveTitle(`${renamedName} · Gripi`);
    await page.screenshot({ path: testInfo.outputPath("renamed.png") });
    releaseSidebar();
    await page.unrouteAll({ behavior: "wait" });
    await page.reload();
    if (isMobile) await activate(page.locator('label[aria-label="Open sessions"]'));
    await expect(row.locator(".session-title")).toHaveText(renamedName);
    await expect(page.locator(".session-header-name")).toHaveText(renamedName);
  } finally {
    releaseSidebar();
    await page.unrouteAll({ behavior: "wait" });
    const response = await page.request.post("/sessions/rename", { form: { session: sessionPath, name: originalName } });
    expect(response.ok()).toBe(true);
  }
});
