import { expect, test } from "@playwright/test";
import { sessions } from "../support/contract.mjs";
import { selectSession } from "../support/ui.mjs";

// Stands in for the gateway's update endpoints; `gateway.down` makes them unreachable.
async function mockGateway(page, status) {
  const gateway = { status, down: false };
  // A page that has loaded again belongs to the restarted instance.
  page.on("framenavigated", () => { gateway.status = { ...gateway.status, instanceId: undefined }; });
  await page.route(/\/gateway-update(\/check)?$/, async (route) => {
    if (gateway.down) return route.abort();
    if (route.request().method() === "POST" && !route.request().url().endsWith("/check")) {
      await gateway.starting;
      gateway.status = gateway.started;
    }
    await route.fulfill({ json: gateway.status });
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
  gateway.started = { state: "updating", message: "Updating Gripi…" };
  await page.route(/\/session_fragment(?:\?|$)/, (route) => gateway.down ? route.abort() : route.continue());
  await page.goto("/");
  await selectSession(page, sessions.history);
  const overlay = page.locator("[data-gateway-update-overlay]");

  await startUpdate(page);

  await expect(overlay).toBeVisible();
  await expect(overlay).toContainText("Updating Gripi…");
  await expect(page.getByLabel("Message to Pi").click({ timeout: 1000 })).rejects.toThrow();
  await page.keyboard.press("Escape");
  await page.keyboard.press("Escape");
  // Checked once, without retrying: the next status poll would reopen a dismissed overlay.
  expect(await overlay.isVisible()).toBe(true);
  await page.keyboard.press("Control+n");
  expect(await page.locator('[data-modal="new-session-modal"]').isVisible()).toBe(false);

  gateway.status = { state: "restarting", message: "Updated to abc12345" };
  await expect(overlay).toContainText("Restarting Gripi…");

  // Going back switches sessions; with the gateway down that must not leave for its error page.
  gateway.down = true;
  await page.evaluate(() => { window.retainedPage = true; });
  await page.goBack();
  await expect(page.locator(".session-reconnect")).toHaveClass(/is-visible/);
  expect(await page.evaluate(() => window.retainedPage)).toBe(true);
  await expect(overlay).toBeVisible();

  const reloaded = page.waitForEvent("load");
  gateway.status = { state: "up_to_date", instanceId: "restarted-instance", currentSha: "abc12345" };
  gateway.down = false;
  await reloaded;
});

test("leaves the page usable while waiting for active sessions and after a failed update", async ({ page }) => {
  const gateway = await mockGateway(page, available);
  gateway.started = { state: "waiting", targetSha: "abc12345", message: "Waiting for 1 active Pi session to finish…" };
  await page.goto("/");
  const overlay = page.locator("[data-gateway-update-overlay]");
  let answerStart;
  gateway.starting = new Promise((resolve) => { answerStart = resolve; });

  await startUpdate(page);

  // Only the gateway knows whether it has to wait, so nothing is blocked before it answers.
  await expect(page.getByText("Starting Gripi update…")).toBeVisible();
  expect(await overlay.isVisible()).toBe(false);
  answerStart();
  await expect(page.getByText("Waiting for 1 active Pi session to finish…")).toBeVisible();
  await expect(overlay).toBeHidden();

  gateway.status = { state: "updating", message: "Updating Gripi…" };
  await expect(overlay).toBeVisible();

  gateway.status = { state: "dependency_failed", message: "The new version did not build." };
  await expect(overlay).toBeHidden();
  await expect(page.getByRole("button", { name: "Retry update" })).toBeVisible();
});

test("joins an update started elsewhere and reloads once the restarted gateway serves the page", async ({ page }) => {
  const gateway = await mockGateway(page, { state: "updating", message: "Updating Gripi…" });
  // Session-only windows have no sidebar, so they can only learn about the update this way.
  await page.goto("/?session_only=1");
  await expect(page.locator("[data-gateway-update-overlay]")).toBeVisible();
  // Slower than the status poll, which must not start the reload over again.
  await page.route(/_gateway_updated=/, async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 2500));
    await route.continue();
  });

  const reloaded = page.waitForEvent("load");
  gateway.status = { state: "up_to_date", instanceId: "restarted-instance", currentSha: "abc12345" };
  await reloaded;
});

const failedStep = {
  state: "dependency_failed",
  currentSha: "old11111",
  targetSha: "abc12345",
  message: "The new version did not build.",
  failure: { step: "Build", exitStatus: 2, timedOut: false, output: `$ mise exec -- go build -o /tmp/${"stage".repeat(40)}/gripi ./cmd/gripi\nundefined: rpc.Start` },
};

test.describe("with touch", () => {
  test.use({ hasTouch: true });

  test("keeps a failed update short in the sidebar and shows the failed step on the first tap", async ({ page, context }) => {
    const gateway = await mockGateway(page, failedStep);
    gateway.started = { state: "waiting", targetSha: "abc12345", message: "Waiting for 1 active Pi session to finish…" };
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);
    await page.goto("/");
    const control = page.locator("[data-gateway-update]");
    const dialog = page.getByRole("dialog", { name: "Update failed" });

    await expect(control).toContainText("Update to abc12345 failed.");
    await expect(control).toContainText("Nothing was changed.");
    await expect(control).not.toContainText("rpc.Start");

    await control.getByRole("button", { name: "Show details" }).tap();
    await expect(dialog).toContainText("The new version did not build.");
    await expect(dialog).toContainText("Nothing was changed. Gripi is still running old11111.");
    await expect(dialog).toContainText("Build · exit 2");
    const output = dialog.locator("pre");
    await expect(output).toHaveText(failedStep.failure.output);
    // A long line scrolls inside the output instead of widening the dialog.
    const [card, block] = [await dialog.boundingBox(), await output.boundingBox()];
    expect(block.x + block.width).toBeLessThanOrEqual(card.x + card.width);

    await dialog.getByRole("button", { name: "Copy details" }).tap();
    await expect(dialog.getByRole("button", { name: "Copied" })).toBeVisible();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(`Update to abc12345 failed\nThe new version did not build.\nBuild · exit 2\n${failedStep.failure.output}`);

    page.once("dialog", (confirmation) => confirmation.accept());
    await dialog.getByRole("button", { name: "Retry update" }).tap();
    await expect(dialog).toBeHidden();
    await expect(page.getByText("Waiting for 1 active Pi session to finish…")).toBeVisible();
    await expect(control.getByRole("button", { name: "Show details" })).toBeHidden();
  });
});

test("keeps the Retry button inside the sidebar under an error with a long unbroken path", async ({ page }) => {
  await mockGateway(page, { state: "error", targetSha: "abc12345", message: `Could not fetch origin master: /srv/${"checkout".repeat(30)}` });
  await page.goto("/");

  const retry = page.getByRole("button", { name: "Retry update" });
  await expect(retry).toBeVisible();
  const [sidebar, button] = [await page.locator(".session-sidebar").boundingBox(), await retry.boundingBox()];
  expect(button.x + button.width).toBeLessThanOrEqual(sidebar.x + sidebar.width);
});
