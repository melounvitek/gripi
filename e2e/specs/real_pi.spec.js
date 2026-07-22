import { randomUUID } from "node:crypto";
import { expect, test } from "@playwright/test";
import { prompts } from "../support/contract.mjs";
import { expectRunFinished, message, sendPrompt } from "../support/ui.mjs";

test("complete one disposable prompt with the installed Pi CLI", async ({ page }) => {
  test.skip(process.env.GRIPI_E2E_REAL_PI !== "1", "Run with `npm run test:e2e:real`");
  test.setTimeout(180_000);
  const token = `GRIPI-E2E-${randomUUID().slice(0, 8).toUpperCase()}`;

  await page.goto("/");
  await page.getByRole("button", { name: "New session" }).click();
  const dialog = page.getByRole("dialog", { name: "New session" });
  await dialog.getByRole("combobox", { name: "Project" }).click();
  await page.getByRole("option", { name: /new-session-desktop/ }).click();
  await dialog.getByRole("button", { name: "Start session" }).click();
  await expect(page).toHaveURL(/[?&]session=/);

  await sendPrompt(page, `${prompts.realPiPrefix} ${token}`);
  await expect(message(page, "assistant", token)).toBeVisible({ timeout: 150_000 });
  await expectRunFinished(page);
});
