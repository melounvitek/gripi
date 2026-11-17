import { expect, test } from "@playwright/test";

const isPendingPoll = (request) => new URL(request.url()).pathname === "/browser-access/pending";

async function askAccess(page) {
  await page.goto("/");
  await page.getByRole("button", { name: "Ask access" }).click();
  return (await page.locator(".code").textContent()).trim();
}

test("the access prompt keeps the request it shows until it's answered", async ({ page, browser, isMobile }) => {
  test.setTimeout(40_000);
  const press = (control) => isMobile ? control.tap() : control.click();
  // Without the cookie of the approved browser.
  const contexts = await Promise.all([1, 2].map(() => browser.newContext({ storageState: { cookies: [], origins: [] } })));
  try {
    const [stranger, device] = await Promise.all(contexts.map((context) => context.newPage()));
    await page.goto("/");
    const prompt = page.locator("[data-browser-access-overlay]");
    const title = page.locator("[data-browser-access-title]");

    const strangerCode = await askAccess(stranger);
    await expect(title).toContainText(strangerCode);
    await press(prompt.getByRole("button", { name: "Deny" }));
    await expect(prompt).toBeHidden();

    const deviceCode = await askAccess(device);
    await expect(title).toContainText(deviceCode);
    await stranger.getByRole("button", { name: "Ask access" }).click();
    await expect(stranger.locator(".code")).toHaveText(strangerCode);
    // The second poll starts once the page has shown the first, which includes the stranger's request.
    await page.waitForRequest(isPendingPoll);
    await page.waitForRequest(isPendingPoll);
    await expect(title).toContainText(deviceCode);

    await press(prompt.getByRole("button", { name: "Allow" }));
    await expect(device.getByText("Browser access required")).toBeHidden();
    await expect(title).toContainText(strangerCode);
    await press(prompt.getByRole("button", { name: "Deny" }));
    await expect(prompt).toBeHidden();
  } finally {
    await Promise.all(contexts.map((context) => context.close()));
  }
});
