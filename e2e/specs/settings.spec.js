import { expect, test } from "@playwright/test";
import { sessions } from "../support/contract.mjs";
import { selectSession } from "../support/ui.mjs";

test("change the model and thinking level", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.settings);
  await page.getByRole("button", { name: "Open model and thinking settings" }).click();

  const dialog = page.getByRole("dialog", { name: "Model & thinking" });
  await expect(dialog).toBeVisible();
  await dialog.getByRole("radio", { name: /Contract Model/ }).check();
  await dialog.getByRole("radio", { name: "high" }).check();
  await dialog.getByRole("button", { name: "Apply" }).click();
  await expect(dialog).toBeHidden();
  await expect(page.getByRole("button", { name: "Open model and thinking settings" })).toContainText("e2e/contract-model (high)");

  await page.reload();
  await expect(page.getByRole("button", { name: "Open model and thinking settings" })).toContainText("e2e/contract-model (high)");
});
