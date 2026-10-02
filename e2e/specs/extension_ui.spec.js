import { expect, test } from "@playwright/test";
import { prompts, replies, sessions } from "../support/contract.mjs";
import { expectRunFinished, message, selectSession, sendPrompt } from "../support/ui.mjs";

test("timeout, retry, and definitive rejection advance the real extension queue", async ({ page }) => {
  const responseAttempts = [];
  await page.route("**/extension_ui_response", async (route) => {
    const id = new URLSearchParams(route.request().postData() || "").get("id");
    responseAttempts.push(id);
    if (id === "e2e-extension-retry") {
      const status = responseAttempts.filter((attempt) => attempt === id).length === 1 ? 503 : 422;
      if (status === 422) await route.fetch();
      await route.fulfill({ status, contentType: "text/plain", body: "intercepted extension response" });
      return;
    }
    await route.continue();
  });

  await page.goto("/");
  await selectSession(page, sessions.extensionRace);
  await sendPrompt(page, prompts.extensionRace);

  await expect(page.getByRole("dialog", { name: "Expiring request" })).toBeVisible();
  const retryDialog = page.getByRole("dialog", { name: "Retry request" });
  await expect(retryDialog).toBeVisible();
  expect(responseAttempts).not.toContain("e2e-extension-expiring");

  await retryDialog.getByRole("option", { name: "Yes" }).click();
  await expect(retryDialog.getByText("Could not answer extension request. Please try again.")).toBeVisible();
  await expect(retryDialog.getByRole("option", { name: "Yes" })).toBeEnabled();

  await retryDialog.getByRole("option", { name: "Yes" }).click();
  const finalDialog = page.getByRole("dialog", { name: "Final queued request" });
  await expect(finalDialog).toBeVisible();
  expect(responseAttempts.filter((id) => id === "e2e-extension-retry")).toHaveLength(2);

  await finalDialog.getByRole("option", { name: "Yes" }).click();
  await expect(finalDialog).toBeHidden();
  await expect(message(page, "assistant", replies.extensionRaceComplete)).toBeVisible();
  await expectRunFinished(page);
});

test("answer an extension confirmation before Pi completes", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.extension);
  await sendPrompt(page, prompts.extension);

  const dialog = page.getByRole("dialog", { name: "Approve release?" });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByText("Allow the deterministic release?", { exact: true })).toBeVisible();
  const options = dialog.getByRole("option");
  await expect(options).toHaveText(["→Yes", "→No"]);
  await expect(options.nth(0)).toBeFocused();
  await page.keyboard.press("Enter");

  await expect(message(page, "assistant", replies.extensionApproved)).toBeVisible();
  await expectRunFinished(page);
});

test("answer select, input, and editor requests from the keyboard", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.extension);
  await sendPrompt(page, prompts.extensionKinds);

  const options = page.getByRole("dialog", { name: "Pick a target" }).getByRole("option");
  await expect(options).toHaveText(["→staging", "→production"]);
  await expect(options.nth(0)).toBeFocused();
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Enter");

  const input = page.getByRole("dialog", { name: "Release name" }).getByRole("textbox");
  await expect(input).toBeFocused();
  await input.fill("v2");
  await input.press("Enter");

  const editor = page.getByRole("dialog", { name: "Release notes" }).getByRole("textbox");
  await expect(editor).toBeFocused();
  await editor.pressSequentially("Notes");
  await editor.press("Shift+Enter");
  await editor.pressSequentially("Shipped");
  await editor.press("Enter");

  await expect(message(page, "assistant", "Extension answers: production / v2 / Notes Shipped")).toBeVisible();
  await expectRunFinished(page);
});
