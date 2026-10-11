import { expect, test } from "@playwright/test";
import { hermes } from "../support/contract.mjs";
import { expectRunFinished, liveMessage, message, selectSession, sendPrompt } from "../support/ui.mjs";

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await selectSession(page, hermes.saved.title);
});

test("a Hermes session shows its saved conversation", async ({ page }) => {
  const { saved } = hermes;
  await expect(message(page, "user", saved.prompt)).toBeVisible();
  await expect(page.locator("article.message--thinking").filter({ hasText: saved.thought })).toBeVisible();
  const tool = page.locator('article[data-tool-call-id="tc-saved"]');
  await expect(tool).toContainText(saved.tool);
  await expect(tool).toContainText(saved.toolOutput);
  await expect(message(page, "assistant", saved.reply)).toBeVisible();
});

test("a Hermes reply streams with its thinking and tool, and stays after a reload", async ({ page }) => {
  const { run } = hermes;
  const prompt = "List the fixture files";
  const reply = `${run.replyPrefix}${prompt}`;
  await sendPrompt(page, prompt);

  await expect(page.locator("#live-output article.message--live.message--thinking").filter({ hasText: run.thought })).toBeVisible();
  await expect(liveMessage(page, "assistant", run.intro)).toBeVisible();
  const tool = liveMessage(page, "tool", run.tool);
  await expect(tool).toContainText(run.toolOutput);
  await expect(tool).not.toHaveClass(/message--tool-error/);
  await expect(liveMessage(page, "assistant", reply)).toBeVisible();
  await expectRunFinished(page);

  await page.reload();
  await expect(message(page, "user", prompt)).toBeVisible();
  await expect(page.locator("article.message--thinking").filter({ hasText: run.thought }).last()).toBeVisible();
  await expect(page.locator("article").filter({ hasText: run.tool }).last()).toContainText(run.toolOutput);
  await expect(message(page, "assistant", reply)).toHaveCount(1);
});

test("stopping Hermes ends its run", async ({ page }) => {
  await sendPrompt(page, hermes.stopPrompt);
  await expect(page.getByRole("button", { name: "Abort running Pi" })).toBeVisible();
  await page.getByRole("button", { name: "Abort running Pi" }).click();
  await expectRunFinished(page);
});

test("Hermes asks in a dialog before a dangerous command, which a reload keeps", async ({ page }) => {
  const { permission } = hermes;
  await sendPrompt(page, permission.prompt);
  let dialog = page.getByRole("dialog", { name: permission.title });
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText("$ rm -rf build");

  await page.reload();
  dialog = page.getByRole("dialog", { name: permission.title });
  await expect(dialog).toBeVisible();
  await dialog.getByRole("option", { name: permission.deny }).click();
  await expect(dialog).toBeHidden();

  // The reload showed the call from the saved session, where it ends.
  const tool = page.locator("article[data-tool-call-id]").filter({ hasText: permission.tool });
  await expect(tool).toContainText(permission.denied);
  await expect(tool).toHaveClass(/message--tool-error/);
  await expectRunFinished(page);
});
