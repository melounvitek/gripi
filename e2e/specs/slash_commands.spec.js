import { expect, test } from "@playwright/test";
import { sessions } from "../support/contract.mjs";
import { selectSession } from "../support/ui.mjs";

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
