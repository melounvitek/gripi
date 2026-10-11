import { expect, test } from "@playwright/test";
import { live, sessions } from "../support/contract.mjs";
import { continueScenario, expectRunFinished, liveMessage, selectSession, sendPrompt } from "../support/ui.mjs";

const scenario = live.footer;

test("live events update the footer model, thinking level and session name", async ({ page }) => {
  let pendingStatusRequests = 0;
  const statusRequest = (request) => new URL(request.url()).pathname === "/status";
  page.on("request", (request) => { if (statusRequest(request)) pendingStatusRequests += 1; });
  for (const settled of ["requestfinished", "requestfailed"]) {
    page.on(settled, (request) => { if (statusRequest(request)) pendingStatusRequests -= 1; });
  }
  await page.goto("/");
  await selectSession(page, sessions.liveFooter);
  const model = page.locator('[data-status-key="model"] .session-status-value');
  await expect(model).toHaveText("e2e/fixture-model (medium)");

  await sendPrompt(page, scenario.prompt);
  await expect(page.getByRole("dialog", { name: scenario.gates.start })).toBeVisible();
  // Let the status refresh after the user message land first, or it could overwrite the footer below.
  await expect.poll(() => pendingStatusRequests).toBe(0);
  await continueScenario(page, scenario.gates.start);
  await expect(page.getByRole("dialog", { name: scenario.gates.streaming })).toBeVisible();
  // The footer follows the streaming assistant message's model, and thinking_level_changed sets the level.
  await expect(model).toHaveText(`${scenario.provider}/${scenario.model} (${scenario.thinkingLevel})`);
  await expect(page.locator(".prompt-form")).toHaveAttribute("data-thinking-level", scenario.thinkingLevel);

  await continueScenario(page, scenario.gates.streaming);
  await expect(page.getByRole("dialog", { name: scenario.gates.ended })).toBeVisible();
  // message_end refreshes the footer from Pi's state.
  await expect(model).toHaveText(`e2e/fixture-model (${scenario.thinkingLevel})`);

  await continueScenario(page, scenario.gates.ended);
  await expect(liveMessage(page, "assistant", scenario.reply)).toBeVisible();
  await expectRunFinished(page);
  // turn_end carries the assistant message too, so its model is back in the footer.
  await expect(model).toHaveText(`${scenario.provider}/${scenario.model} (${scenario.thinkingLevel})`);
  // session_info_changed renames the header and the tab, and refreshes the sidebar.
  await expect(page.getByRole("heading", { level: 1, name: scenario.renamed })).toBeVisible();
  await expect(page).toHaveTitle(`${scenario.renamed} · Gripi`);
  await expect(page.getByRole("link", { name: new RegExp(scenario.renamed) })).toBeVisible();
});
