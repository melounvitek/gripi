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

  await retryDialog.getByRole("button", { name: "Confirm" }).click();
  await expect(retryDialog.getByText("Could not answer extension request. Please try again.")).toBeVisible();
  await expect(retryDialog.getByRole("button", { name: "Confirm" })).toBeEnabled();

  await retryDialog.getByRole("button", { name: "Confirm" }).click();
  const finalDialog = page.getByRole("dialog", { name: "Final queued request" });
  await expect(finalDialog).toBeVisible();
  expect(responseAttempts.filter((id) => id === "e2e-extension-retry")).toHaveLength(2);

  await finalDialog.getByRole("button", { name: "Confirm" }).click();
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
  await dialog.getByRole("button", { name: "Confirm" }).click();

  await expect(message(page, "assistant", replies.extensionApproved)).toBeVisible();
  await expectRunFinished(page);
});
