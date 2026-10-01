import { expect, test } from "@playwright/test";
import { sessions } from "../support/contract.mjs";
import { selectSession } from "../support/ui.mjs";

test("change the model and thinking level", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.settings);
  // Pi CLI colours the editor's rules by thinking level: medium, then high.
  const editor = page.locator(".composer-input-row");
  const composer = page.getByLabel("Message to Pi");
  await composer.focus();
  await expect(editor).toHaveCSS("border-top-color", "rgb(129, 162, 190)");
  const modelButton = page.getByRole("button", { name: "Open model and thinking settings" });
  await modelButton.click();

  // Like Pi CLI's /model: the picker opens on the scoped models with the cursor on the current one.
  const dialog = page.getByRole("dialog", { name: "Model & thinking" });
  const options = dialog.getByRole("option");
  const status = dialog.getByRole("status");
  await expect(dialog.getByRole("button", { name: "scoped" })).toHaveAttribute("aria-pressed", "true");
  // Scoped is listed first because the picker opens on it; Pi CLI itself prints "all | scoped".
  await expect(dialog.getByRole("button", { name: /^(scoped|all)$/ })).toHaveText(["scoped", "all"]);
  await expect(options).toHaveText([/fixture-model \[e2e\]/, /contract-model \[e2e\]/]);
  await expect(dialog.getByRole("option", { selected: true })).toHaveAttribute("aria-current", "true");
  await expect(status).toHaveText("Model Name: Fixture Model");

  // Tab switches the scope, search narrows it, and Escape leaves the settings alone.
  await page.keyboard.press("Tab");
  await expect(dialog.getByRole("button", { name: "all" })).toHaveAttribute("aria-pressed", "true");
  await expect(options).toHaveCount(3);
  await page.keyboard.type("long provider");
  await expect(options).toHaveText([/claude-opus-long-model-name/]);
  await page.keyboard.type("x");
  await expect(status).toHaveText("No matching models");
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
  await expect(modelButton).toContainText("e2e/fixture-model (medium)");

  // Arrow keys wrap around the list and Enter applies the model right away.
  await modelButton.click();
  await expect(options).toHaveCount(2);
  await page.keyboard.press("ArrowUp");
  await expect(dialog.getByRole("option", { selected: true })).toContainText("contract-model");
  await expect(status).toHaveText("Model Name: Contract Model");
  await page.keyboard.press("Enter");
  await expect(dialog).toBeHidden();
  await expect(modelButton).toContainText("e2e/contract-model (medium)");

  await modelButton.click();
  await expect(dialog.getByRole("option", { selected: true })).toContainText("contract-model");
  await expect(dialog.getByRole("button", { name: "medium" })).toHaveAttribute("aria-pressed", "true");
  await dialog.getByRole("button", { name: "high", exact: true }).click();
  await expect(dialog).toBeHidden();
  await expect(modelButton).toContainText("e2e/contract-model (high)");
  await composer.focus();
  await expect(editor).toHaveCSS("border-top-color", "rgb(178, 148, 187)");

  await page.reload();
  await expect(modelButton).toContainText("e2e/contract-model (high)");
  await composer.focus();
  await expect(editor).toHaveCSS("border-top-color", "rgb(178, 148, 187)");
});
