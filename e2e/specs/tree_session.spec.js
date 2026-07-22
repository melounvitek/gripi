import { expect, test } from "@playwright/test";
import { sessions } from "../support/contract.mjs";
import { selectSession } from "../support/ui.mjs";

test("open, label, and navigate the native Pi session tree", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.prompt);
  const composer = page.getByPlaceholder("Ask Pi…");
  await composer.fill("/tree");
  const treeCommand = page.locator('.command[data-command-name="tree"]');
  await expect(treeCommand).toBeVisible();
  await treeCommand.click();
  await composer.press("Enter");

  const dialog = page.getByRole("dialog", { name: "Session tree" });
  await expect(dialog).toBeVisible();
  await expect(dialog.locator("[data-tree-session-status]")).toContainText(/entries?\./);
  const firstEntry = dialog.locator("[data-tree-viewport] > [role=treeitem] > .tree-session-row [data-tree-entry-id]");
  await expect(firstEntry).toBeVisible();
  await firstEntry.click();

  await dialog.getByText("Search & options").click();
  await dialog.getByPlaceholder("Optional label").fill("E2E checkpoint");
  await dialog.getByRole("button", { name: "Save label" }).click();
  await expect(dialog.locator("[data-tree-session-status]")).toHaveText("Label updated.");
  await expect(dialog.locator('[role="treeitem"][aria-selected="true"] > .tree-session-row .tree-session-meta')).toContainText("E2E checkpoint");

  await dialog.locator("[data-tree-navigate]").click();
  await expect(dialog.getByText("Choose how to prepare the branch context.")).toBeVisible();
  await dialog.locator("[data-tree-summary-submit]").click();

  await expect(dialog).toBeHidden();
  await expect(page.getByPlaceholder("Ask Pi…")).toHaveValue(`Fixture question for ${sessions.prompt}`);
});
