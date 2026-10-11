import { expect, test } from "@playwright/test";
import { live, sessions } from "../support/contract.mjs";
import { continueScenario, expectRunFinished, liveMessage, selectSession, sendPrompt } from "../support/ui.mjs";

const scenario = live.customMessages;

test("live custom messages show displayed notes under their type", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.liveCustomMessages);
  await sendPrompt(page, scenario.prompt);
  await expect(page.getByRole("dialog", { name: scenario.gate })).toBeVisible();

  const notes = page.locator('#live-output article.message--live[data-role="custom"]');
  // Each note's message_start and message_end share one card; the repeated note has a new timestamp, so it adds one.
  await expect(notes).toHaveCount(2);
  await expect(notes.locator(".role")).toHaveText([`[${scenario.customType}]`, `[${scenario.customType}]`]);
  await expect(notes.nth(0)).toHaveClass(/message--status/);
  await expect(notes.locator(".message-body--markdown strong")).toHaveText(["Displayed", "Displayed"]);
  // display: false keeps a note out of the conversation.
  await expect(page.locator("#live-output").getByText(scenario.hidden)).toHaveCount(0);

  await continueScenario(page, scenario.gate);
  await expect(liveMessage(page, "assistant", scenario.reply)).toBeVisible();
  await expectRunFinished(page);
});
