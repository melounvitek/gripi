import { expect, test } from "@playwright/test";
import { prompts, sessions } from "../support/contract.mjs";
import { expectRunFinished, selectSession, sendPrompt } from "../support/ui.mjs";

test("pick a fork point from the keyboard like Pi's /fork selector", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.fork);
  await sendPrompt(page, prompts.standard);
  await expectRunFinished(page);
  await sendPrompt(page, "/fork");

  const dialog = page.getByRole("dialog", { name: "Fork session" });
  const options = dialog.getByRole("option");
  await expect(options).toHaveCount(2);
  await expect(options.nth(1)).toHaveAttribute("aria-selected", "true");
  await expect(options.nth(1)).toContainText("Message 2 of 2");
  await expect(options.nth(1).locator(".picker-cursor")).toBeVisible();
  await expect(options.nth(0).locator(".picker-cursor")).toBeHidden();

  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
  await sendPrompt(page, "/fork");
  await expect(options.nth(1)).toBeFocused();

  await page.keyboard.press("ArrowUp");
  await expect(options.nth(0)).toHaveAttribute("aria-selected", "true");
  await page.keyboard.press("ArrowUp");
  await expect(options.nth(1)).toHaveAttribute("aria-selected", "true");
  await page.keyboard.press("ArrowDown");
  await expect(options.nth(0)).toHaveAttribute("aria-selected", "true");
  await page.keyboard.press("Enter");

  await expect(dialog).toBeHidden();
  await expect(page.getByLabel("Message to Pi")).toHaveValue(`Fixture question for ${sessions.fork}`);
});
