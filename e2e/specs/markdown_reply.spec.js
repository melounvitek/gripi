import { expect, test } from "@playwright/test";
import { prompts, replies, sessions } from "../support/contract.mjs";
import { expectRunFinished, message, selectSession, sendPrompt } from "../support/ui.mjs";

test("a reply's headings leave the app's elements alone and its task list keeps its markers", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.markdownHeadings);
  await sendPrompt(page, prompts.markdownHeadings);
  await expectRunFinished(page);

  const composer = page.getByLabel("Message to Pi");
  for (const [index, stage] of ["live", "reloaded history"].entries()) {
    await test.step(stage, async () => {
      if (stage === "reloaded history") await page.reload();
      const response = message(page, "assistant", "Headings stay in the conversation.");
      for (const name of ["Live output", "Command list", "Abort form"]) {
        await expect(response.getByRole("heading", { name })).toBeVisible();
      }
      await expect(response.getByRole("listitem")).toHaveText(["[x] done task", "[ ] open task"]);

      await sendPrompt(page, prompts.standard);
      await expect(message(page, "assistant", replies.standard)).toHaveCount(index + 1);
      await expectRunFinished(page);

      await composer.fill("/");
      await expect(page.locator('.command[data-command-name="model"]')).toBeVisible();
    });
  }
});
