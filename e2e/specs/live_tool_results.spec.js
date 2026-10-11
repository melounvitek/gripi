import { expect, test } from "@playwright/test";
import { live, sessions } from "../support/contract.mjs";
import { continueScenario, expectRunFinished, liveMessage, selectSession, sendPrompt } from "../support/ui.mjs";

const scenario = live.toolResults;
const { readOk, readError, editOk, editError, writeOk, writeError } = scenario;
const tools = [readOk, readError, editOk, editError, writeOk, writeError];

function toolCard(page, tool) {
  return page.locator(`#live-output article.message--live[data-tool-call-id="${tool.id}"]`);
}

function outputBody(page, tool) {
  return toolCard(page, tool).locator("[data-tool-output-body]");
}

function outputLines(page, tool) {
  return outputBody(page, tool).locator(".tool-diff-line, .tool-output-line");
}

test("live read, edit and write results merge into their call cards", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.liveToolResults);
  await sendPrompt(page, scenario.prompt);
  await expect(page.getByRole("dialog", { name: scenario.gates.results })).toBeVisible();

  for (const tool of tools) await expect(toolCard(page, tool)).toHaveClass(/message--tool-pending/);
  await expect(toolCard(page, readOk).locator(".compact-summary")).toHaveText(`read ${readOk.path}`);
  await expect(toolCard(page, readOk).locator("[data-tool-output-collapse]")).toBeHidden();
  await expect(outputLines(page, editOk)).toHaveText(["Edit 1", `- ${editOk.oldText}`, `+ ${editOk.newText}`]);
  await expect(outputBody(page, editOk)).toHaveClass(/message-body--edit-preview/);
  await expect(outputLines(page, writeOk)).toHaveText([`+ ${writeOk.content}`]);

  await continueScenario(page, scenario.gates.results);
  await expect(page.getByRole("dialog", { name: scenario.gates.finish })).toBeVisible();
  for (const tool of tools) {
    // A paired result updates the call card instead of adding a result card.
    await expect(toolCard(page, tool)).toHaveCount(1);
    await expect(toolCard(page, tool)).not.toHaveClass(/message--tool-pending/);
    await expect(toolCard(page, tool)).toHaveAttribute("data-activity-completed", "");
  }

  // A successful read hides its output.
  await expect(toolCard(page, readOk)).not.toHaveClass(/message--tool-error/);
  await expect(toolCard(page, readOk).locator("[data-tool-output-collapse]")).toBeHidden();
  await expect(toolCard(page, readOk)).not.toContainText(readOk.result);

  // A successful edit replaces its preview with Pi's diff.
  await expect(toolCard(page, editOk)).not.toHaveClass(/message--tool-error/);
  await expect(outputBody(page, editOk)).not.toHaveClass(/message-body--edit-preview/);
  await expect(outputLines(page, editOk)).toHaveText(editOk.diff.split("\n"));
  await expect(outputLines(page, editOk).filter({ hasText: `-2 ${editOk.oldText}` })).toHaveClass(/tool-diff-line--remove/);
  await expect(outputLines(page, editOk).filter({ hasText: `+2 ${editOk.newText}` })).toHaveClass(/tool-diff-line--add/);

  // Other results are appended to the call card once, though Pi sends them in both message_start and message_end.
  await expect(toolCard(page, readError)).toHaveClass(/message--tool-error/);
  await expect(outputLines(page, readError)).toHaveText([readError.result]);

  // A failed edit keeps its preview.
  await expect(toolCard(page, editError)).toHaveClass(/message--tool-error/);
  await expect(outputBody(page, editError)).toHaveClass(/message-body--edit-preview/);
  await expect(outputLines(page, editError)).toHaveText(["Edit 1", `- ${editError.oldText}`, `+ ${editError.newText}`, "", editError.result]);

  await expect(toolCard(page, writeOk)).not.toHaveClass(/message--tool-error/);
  await expect(outputLines(page, writeOk)).toHaveText([`+ ${writeOk.content}`, "", writeOk.result]);

  await expect(toolCard(page, writeError)).toHaveClass(/message--tool-error/);
  await expect(outputBody(page, writeError)).not.toHaveClass(/message-body--edit-preview/);
  await expect(outputLines(page, writeError)).toHaveText([`+ ${writeError.content}`, "", writeError.result]);

  await continueScenario(page, scenario.gates.finish);
  await expect(liveMessage(page, "assistant", scenario.reply)).toBeVisible();
  await expectRunFinished(page);
});
