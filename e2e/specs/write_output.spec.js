import { expect, test } from "@playwright/test";
import { prompts, sessions, writeTool } from "../support/contract.mjs";
import { expectRunFinished, selectSession, sendPrompt } from "../support/ui.mjs";

test("expands complete live and restored write output", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.writeOutput);
  await sendPrompt(page, prompts.writeOutput);

  const card = writeCard(page);
  await expect(card).toContainText("latest-write-content");
  await expectRunFinished(page);
  await verifyCompleteWrite(card);

  await page.reload();
  await verifyCompleteWrite(writeCard(page));
});

function writeCard(page) {
  return page.locator(".message--tool-call").filter({ hasText: `write ${writeTool.path}` }).last();
}

async function verifyCompleteWrite(card) {
  await card.getByRole("button", { name: "Expand" }).click();
  const region = card.getByRole("region", { name: "Expanded tool output" });
  await expect(region).toContainText("oldest-write-content");
  await expect(region).toContainText("latest-write-content");
  await expect(region).toContainText(writeTool.result);
}
