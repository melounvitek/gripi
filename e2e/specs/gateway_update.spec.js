import { expect, test } from "@playwright/test";
import { FIXTURE_MARKER } from "../support/contract.mjs";

// Stands in for the gateway's update endpoints; `gateway.down` makes them unreachable.
async function mockGateway(page, status) {
  const gateway = { status, down: false, polls: 0 };
  await page.route(/\/gateway-update(\/check)?$/, async (route) => {
    if (gateway.down) {
      gateway.polls += 1;
      return route.abort();
    }
    if (route.request().method() === "POST" && !route.request().url().endsWith("/check")) gateway.status = gateway.started;
    const status = gateway.status;
    // Report the restarted instance once; the reloaded page then belongs to it.
    if (status.instanceId) gateway.status = { ...status, instanceId: undefined };
    await route.fulfill({ json: status });
  });
  return gateway;
}

async function startUpdate(page) {
  page.once("dialog", (dialog) => dialog.accept());
  await page.getByRole("button", { name: "Update to abc12345" }).click();
}

const available = { state: "available", targetSha: "abc12345", message: "1 update commit available" };

test("blocks the page while the gateway updates and reloads it after the restart", async ({ page }) => {
  const gateway = await mockGateway(page, available);
  gateway.started = { state: "updating", message: "Updating gateway…" };
  await page.goto("/");
  const overlay = page.locator("[data-gateway-update-overlay]");
  await expect(overlay).toBeHidden();

  await startUpdate(page);

  await expect(overlay).toBeVisible();
  await expect(overlay).toContainText("Updating gateway…");
  await expect(page.getByText(FIXTURE_MARKER, { exact: true }).click({ timeout: 1000 })).rejects.toThrow();
  await page.keyboard.press("Escape");
  await page.keyboard.press("Escape");
  // Checked once, without retrying: the next status poll would reopen a dismissed overlay.
  expect(await overlay.isVisible()).toBe(true);

  gateway.status = { state: "restarting", message: "Updated to abc12345" };
  await expect(overlay).toContainText("Restarting gateway…");
  await expect(overlay).not.toContainText("Updated to");

  gateway.down = true;
  await expect.poll(() => gateway.polls).toBeGreaterThan(1);
  await expect(overlay).toBeVisible();

  const reloaded = page.waitForEvent("load");
  gateway.status = { state: "up_to_date", instanceId: "restarted-instance", currentSha: "abc12345" };
  gateway.down = false;
  await reloaded;
  await expect(page.getByText(FIXTURE_MARKER, { exact: true })).toBeVisible();
  await expect(overlay).toBeHidden();
});

test("leaves the page usable while waiting for active sessions and after a failed update", async ({ page }) => {
  const gateway = await mockGateway(page, available);
  gateway.started = { state: "waiting", targetSha: "abc12345", message: "Waiting for 1 active Pi session to finish…" };
  await page.goto("/");
  const overlay = page.locator("[data-gateway-update-overlay]");

  await startUpdate(page);

  await expect(page.getByText("Waiting for 1 active Pi session to finish…")).toBeVisible();
  await expect(overlay).toBeHidden();

  gateway.status = { state: "updating", message: "Updating gateway…" };
  await expect(overlay).toBeVisible();

  gateway.status = { state: "dependency_failed", message: "Update validation failed before changing the live checkout" };
  await expect(overlay).toBeHidden();
  await expect(page.getByRole("button", { name: "Retry update" })).toBeVisible();
});
