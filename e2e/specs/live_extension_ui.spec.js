import { expect, test } from "@playwright/test";
import { live, sessions } from "../support/contract.mjs";
import { continueScenario, expectRunFinished, liveMessage, selectSession, sendPrompt } from "../support/ui.mjs";

const scenario = live.extensionUi;
const { above, implicit, below } = scenario.widgets;

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.liveExtensionUi);
});

async function expectWidgets(page, placement, expected) {
  const widgets = page.locator(`[data-extension-widgets-${placement}] .extension-widget`);
  await expect(widgets).toHaveCount(expected.length);
  for (const [index, widget] of expected.entries()) {
    await expect(widgets.nth(index)).toHaveAttribute("data-extension-widget-key", widget.key);
    await expect(widgets.nth(index)).toHaveText(`${widget.key}${widget.lines.join("\n")}`);
  }
}

test("extension requests that need no answer update widgets, title, notices and the editor", async ({ page }) => {
  const baseTitle = `${sessions.liveExtensionUi} · Gripi`;
  await expect(page).toHaveTitle(baseTitle);
  const confirmations = [];
  let acceptConfirmation = false;
  page.on("dialog", async (dialog) => {
    confirmations.push(dialog.message());
    if (acceptConfirmation) await dialog.accept();
    else await dialog.dismiss();
  });
  const composer = page.getByLabel("Message to Pi");

  await sendPrompt(page, scenario.prompt);
  await expect(page.getByRole("dialog", { name: scenario.gates.removal })).toBeVisible();
  // Widgets without a placement go above the editor.
  await expectWidgets(page, "above", [above, implicit]);
  await expectWidgets(page, "below", [below]);
  await expect(page).toHaveTitle(scenario.title);
  await expect(liveMessage(page, "status", scenario.notices.info)).toHaveCount(1);
  await expect(liveMessage(page, "status", `Warning: ${scenario.notices.warning}`)).toHaveCount(1);
  await expect(liveMessage(page, "error", scenario.notices.error)).toHaveClass(/message--error/);
  // An empty editor takes the text without asking.
  await expect(composer).toHaveValue(scenario.editorText.empty);
  expect(confirmations).toEqual([]);

  acceptConfirmation = true;
  await continueScenario(page, scenario.gates.removal);
  await expect(page.getByRole("dialog", { name: scenario.gates.decline })).toBeVisible();
  await expectWidgets(page, "above", [above]);
  await expectWidgets(page, "below", [below]);
  await expect(page).toHaveTitle(baseTitle);
  await expect(composer).toHaveValue(scenario.editorText.replaced);
  expect(confirmations).toEqual(["Extension wants to replace your draft. Apply it?"]);

  acceptConfirmation = false;
  await continueScenario(page, scenario.gates.decline);
  await expect(liveMessage(page, "assistant", scenario.reply)).toBeVisible();
  expect(confirmations).toHaveLength(2);
  await expect(composer).toHaveValue(scenario.editorText.replaced);
  // An error notice is only a card; it leaves the composer's state alone.
  await expectRunFinished(page);
});

test("a page reload restores extension widgets and title, and Pi reload clears them", async ({ page }) => {
  await sendPrompt(page, scenario.restorePrompt);
  const gate = page.getByRole("dialog", { name: scenario.gates.restore });
  await expect(gate).toBeVisible();
  await expectWidgets(page, "above", [above]);
  await expectWidgets(page, "below", [below]);
  await expect(page).toHaveTitle(scenario.title);

  await page.reload();
  await expect(gate).toBeVisible();
  await expectWidgets(page, "above", [above]);
  await expectWidgets(page, "below", [below]);
  await expect(page).toHaveTitle(scenario.title);

  await continueScenario(page, scenario.gates.restore);
  const reply = liveMessage(page, "assistant", scenario.restoreReply);
  await expect(reply).toBeVisible();
  await expectRunFinished(page);

  // Reloading Pi's resources sends extension_ui_reset. Pi retires 2 s after the run, so reload at once.
  await page.getByLabel("Message to Pi").fill("/reload");
  await page.locator(".prompt-form").evaluate((form) => form.requestSubmit());
  await expect(page.locator(".composer-state")).toHaveText("Reloaded");
  await expect(page.locator("[data-extension-widgets-above]")).toBeHidden();
  await expect(page.locator("[data-extension-widgets-below]")).toBeHidden();
  await expect(page).not.toHaveTitle(scenario.title);
  // The live reply is still there, so the reset cleared them rather than a refresh after Pi retired.
  await expect(reply).toBeVisible();
});
