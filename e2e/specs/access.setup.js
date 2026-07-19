import { expect, test as setup } from "@playwright/test";
import { ADMIN_PASSWORD, FIXTURE_MARKER } from "../support/contract.mjs";

const authStatePath = process.env.GRIPI_E2E_AUTH_STATE;
if (!authStatePath) throw new Error("GRIPI_E2E_AUTH_STATE is required");

setup("approve the browser and verify the disposable fixture", async ({ page }) => {
  const liveStatus = page.waitForResponse((response) => new URL(response.url()).pathname === "/status" && response.ok());
  await page.goto("/");
  const accessHeading = page.getByRole("heading", { name: "Browser access required" });
  const accessRequired = await accessHeading.isVisible();

  if (process.env.GRIPI_E2E_EXPECT_ACCESS === "1") await expect(accessHeading).toBeVisible();
  if (accessRequired) {
    await page.getByPlaceholder("Admin password").fill(process.env.GRIPI_E2E_ADMIN_PASSWORD || ADMIN_PASSWORD);
    await page.getByRole("button", { name: "Approve this browser" }).click();
  }

  await expect(page.getByRole("img", { name: "Gripi" })).toBeVisible();
  await expect(page.getByText(FIXTURE_MARKER, { exact: true })).toBeVisible();
  await liveStatus;
  await expect(page.getByRole("button", { name: "Open model and thinking settings" })).toContainText("e2e/fixture-model");
  await page.context().storageState({ path: authStatePath });
});
