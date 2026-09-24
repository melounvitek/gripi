import { expect, test } from "@playwright/test";
import { prompts, replies, sessions } from "../support/contract.mjs";
import { expectRunFinished, message, selectSession, sendPrompt } from "../support/ui.mjs";

test("filter slash commands by name rather than description", async ({ page }) => {
  await page.route("**/commands?**", (route) => route.fulfill({
    contentType: "text/html",
    body: `<details id="command-list" class="command-list" data-loaded="true">
      <summary>Slash commands (2)</summary><h3>Skill commands</h3>
      <div class="command" data-command-name="skill:pr-preparation" data-command-text="skill:pr-preparation Prepares a branch for pull request review">/skill:pr-preparation — Prepares a branch for pull request review</div>
      <div class="command" data-command-name="skill:pr-review" data-command-text="skill:pr-review Review pull requests and prepare comments">/skill:pr-review — Review pull requests and prepare comments</div>
    </details>`
  }));
  await page.goto("/");
  await selectSession(page, sessions.prompt);

  const composer = page.getByLabel("Message to Pi");
  const matches = page.locator(".command:visible");
  await composer.fill("/PREPAR");
  await expect(matches).toHaveCount(1);
  await expect(matches).toHaveAttribute("data-command-name", "skill:pr-preparation");
  await page.screenshot({ path: test.info().outputPath("slash-name-filter.png") });

  await composer.fill("/prepare");
  await expect(matches).toHaveCount(0);
  await composer.fill("/review");
  await expect(matches).toHaveCount(1);
  await expect(matches).toHaveAttribute("data-command-name", "skill:pr-review");
  await composer.fill("/");
  await expect(matches).toHaveCount(2);
});

test("Enter applies a highlighted built-in slash command", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.prompt);
  const composer = page.getByLabel("Message to Pi");
  await composer.fill("/mod");
  await expect(page.locator(".command.is-highlighted")).toHaveAttribute("data-command-name", "model");
  await composer.press("Enter");
  await expect(page.getByRole("dialog", { name: "Model & thinking" })).toBeVisible();
  await expect(composer).toHaveValue("");
});

test("Enter submits the arrow-selected slash command exactly once", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.prompt);
  const composer = page.getByLabel("Message to Pi");
  await composer.fill("/log");
  await expect(page.locator(".command:visible")).toHaveCount(2);
  await expect(page.locator(".command.is-highlighted")).toHaveAttribute("data-command-name", "login");
  await composer.press("ArrowDown");
  await expect(page.locator(".command.is-highlighted")).toHaveAttribute("data-command-name", "logout");
  const submissions = [];
  page.on("request", (request) => {
    if (new URL(request.url()).pathname === "/prompt") submissions.push(request);
  });
  await composer.press("Enter");
  await expect(message(page, "gateway", "/logout isn’t available in Gripi.")).toBeVisible();
  await expectRunFinished(page);
  expect(submissions).toHaveLength(1);
  expect(submissions[0].postData()).toContain("/logout");
});

test("Tab completes a slash command and Enter preserves its arguments", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.prompt);
  const composer = page.getByLabel("Message to Pi");
  await composer.fill("/nam");
  await expect(page.locator(".command.is-highlighted")).toHaveAttribute("data-command-name", "name");
  await composer.press("Tab");
  await expect(composer).toHaveValue("/name ");
  await expect(composer).toBeFocused();
  await composer.pressSequentially(sessions.prompt);
  const response = page.waitForResponse("**/prompt");
  await composer.press("Enter");
  expect((await response).request().postData()).toContain(`/name ${sessions.prompt}`);
  await expect(composer).toHaveValue("");
  await expect(page.getByRole("heading", { level: 1, name: sessions.prompt })).toBeVisible();
});

test("Shift+Enter adds a newline and clicking a slash command only completes it", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.prompt);
  const composer = page.getByLabel("Message to Pi");
  const modelCommand = page.locator('.command[data-command-name="model"]');
  await composer.fill("/mod");
  await expect(modelCommand).toBeVisible();
  await composer.press("Shift+Enter");
  await expect(composer).toHaveValue("/mod\n");
  await modelCommand.click();
  await expect(composer).toHaveValue("/model ");
  await expect(page.getByRole("dialog", { name: "Model & thinking" })).toBeHidden();
});

for (const [key, session, start, reply, delivery] of [
  ["Enter", sessions.controlsSteer, prompts.steerStart, replies.steer, "steer"],
  ["Alt+Enter", sessions.controlsFollowUp, prompts.followUpStart, replies.followUp, "follow_up"]
]) {
  test(`${key} submits a highlighted slash command with ${delivery} delivery during a run`, async ({ page }) => {
    await page.goto("/");
    await selectSession(page, session);
    const responses = message(page, "assistant", reply);
    const previousCount = await responses.count();
    await sendPrompt(page, start);
    await expect(page.getByRole("button", { name: "Abort running Pi" })).toBeVisible();
    const composer = page.getByLabel("Message to Pi");
    await composer.fill("/steer-t");
    await expect(page.locator(".command.is-highlighted")).toHaveAttribute("data-command-name", "steer-template");
    const response = page.waitForResponse("**/prompt");
    await composer.press(key);
    const body = (await response).request().postData();
    expect(body).toContain("/steer-template");
    expect(body).toContain(`name="streaming_behavior"\r\n\r\n${delivery}\r\n`);
    await expect(responses).toHaveCount(previousCount + 1);
    await expect(responses.last()).toBeVisible();
    await expectRunFinished(page);
  });
}
