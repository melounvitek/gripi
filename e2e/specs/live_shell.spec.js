import { expect, test } from "@playwright/test";
import { nativeBash, prompts, replies, sessions } from "../support/contract.mjs";
import { activityView, expectRunFinished, liveMessage, selectSession, sendPrompt } from "../support/ui.mjs";

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.liveShell);
});

function shellCard(page, command) {
  return page.locator('#live-output article.message--live[data-role="bashExecution"]').filter({ hasText: `$ ${command}` });
}

function statusItems(card) {
  return card.getByRole("status", { name: "Shell command status" }).locator(".bash-execution-status-item");
}

test("a shell command's output shows as it arrives, also after a reload and in Brief view", async ({ page }) => {
  const { command, chunks } = nativeBash.streaming;
  const [first, second] = chunks.map((chunk) => chunk.trim());
  let releasePrompt;
  const promptHeld = new Promise((resolve) => { releasePrompt = resolve; });
  await page.route("**/prompt", async (route) => {
    await promptHeld;
    await route.continue();
  });
  await sendPrompt(page, `!${command}`);
  // Like Pi CLI, the command runs at once, before the gateway has even received it.
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "bash");
  await expect(page.locator(".composer-state")).toHaveText("Shell command running…");
  releasePrompt();

  const card = shellCard(page, command);
  const lines = card.locator(".tool-output-line");
  await expect(lines.first()).toHaveText(first);
  await expect(statusItems(card)).toHaveText(["running"]);
  await activityView(page, "Brief").click();
  await expect(lines.first()).toBeVisible();

  // The page restores the output so far, and the rest keeps coming.
  await page.reload();
  await expect(lines.first()).toHaveText(first);
  await expect(lines.first()).toBeVisible();
  await expect(statusItems(card)).toHaveText(["running"]);
  await expect(lines).toHaveText([first, second]);
  await expect(statusItems(card)).toHaveText(["running"]);

  await page.getByRole("button", { name: "Abort running Pi" }).click();
  await expect(statusItems(card)).toHaveText(["cancelled"]);
  await expect(lines).toHaveText([first, second]);
  await expectRunFinished(page);
});

test("a shell command that fails to start leaves a prompt sent meanwhile sending", async ({ page }) => {
  const { command } = nativeBash.excluded;
  const held = [];
  await page.route("**/prompt", (route) => { held.push(route); });
  await sendPrompt(page, `!${command}`);
  await expect.poll(() => held.length).toBe(1);
  await sendPrompt(page, prompts.standard);
  await expect.poll(() => held.length).toBe(2);
  const state = page.locator(".composer-state");
  await expect(state).toHaveAttribute("data-state", "sending");

  await held[0].fulfill({ status: 500, body: "Unavailable" });
  await expect(page.getByLabel("Message to Pi")).toHaveValue(`!${command}`);
  await expect(state).toHaveAttribute("data-state", "sending");

  await held[1].continue();
  await expect(liveMessage(page, "assistant", replies.standard)).toBeVisible();
  await expectRunFinished(page);
});

test("a shell command Pi fails to run shows the error", async ({ page }) => {
  const { command, error } = nativeBash.failure;
  await sendPrompt(page, `!${command}`);
  const card = shellCard(page, command);
  await expect(statusItems(card)).toHaveText(["failed"]);
  await expect(card).toHaveClass(/message--tool-error/);
  await expect(card.locator(".tool-output-line")).toHaveText([error]);
  await expectRunFinished(page);
});

test("a non-zero exit marks the shell card failed with its code", async ({ page }) => {
  const { command, output } = nativeBash.nonzero;
  await sendPrompt(page, `!${command}`);
  const card = shellCard(page, command);
  await expect(statusItems(card)).toHaveText(["exit 7"]);
  await expect(card).toHaveClass(/message--tool-error/);
  await expect(card.locator(".tool-output-line")).toHaveText([output.trim()]);
  await expectRunFinished(page);
});

test("truncated shell output names its full output file", async ({ page }) => {
  const { command, output, fullOutputFile } = nativeBash.truncated;
  await sendPrompt(page, `!${command}`);
  const card = shellCard(page, command);
  await expect(statusItems(card)).toHaveText(["output truncated", `full output: ~/${fullOutputFile}`]);
  await expect(card).toHaveClass(/message--bash-truncated/);
  await expect(card).not.toHaveClass(/message--tool-error/);
  await expect(card.locator(".tool-output-line")).toHaveText(output.trim().split("\n"));
  await expectRunFinished(page);
});

test("polled events for a shell the response already finished do not replay it", async ({ page }) => {
  const { command } = nativeBash.nonzero;
  let releaseEvents;
  let pollHeld;
  const eventsHeld = new Promise((resolve) => { releaseEvents = resolve; });
  const pollParked = new Promise((resolve) => { pollHeld = resolve; });
  await page.route(/\/events(?:\?|$)/, async (route) => {
    pollHeld();
    await eventsHeld;
    await route.continue();
  });
  // Park a poll first, so no poll can bring the shell's events before the prompt response finishes the card.
  await page.evaluate(() => window.dispatchEvent(new Event("pageshow")));
  await pollParked;

  await sendPrompt(page, `!${command}`);
  const card = shellCard(page, command);
  await expect(statusItems(card)).toHaveText(["exit 7"]);

  const replay = page.waitForResponse(async (response) => /\/events(?:\?|$)/.test(new URL(response.url()).pathname) &&
    (await response.json()).events.some((event) => event.type === "bash_start"));
  releaseEvents();
  await replay;
  await page.unroute(/\/events(?:\?|$)/);
  // The next poll starts only after the replayed batch is rendered.
  await page.waitForRequest(/\/events(?:\?|$)/);
  await expect(card).toHaveCount(1);
  await expect(statusItems(card)).toHaveText(["exit 7"]);
  await expectRunFinished(page);
});

test("a shell finished during a run is restored once after a page reload", async ({ page }) => {
  const { command } = nativeBash.nonzero;
  await sendPrompt(page, prompts.abortStart);
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "running");
  await sendPrompt(page, `!${command}`);
  await expect(statusItems(shellCard(page, command))).toHaveText(["exit 7"]);

  // Pi saves the shell only after the run, so the page restores it from the live snapshot.
  await page.reload();
  const restored = shellCard(page, command);
  await expect(restored).toHaveCount(1);
  await expect(statusItems(restored)).toHaveText(["exit 7"]);
  await expect(restored).toHaveClass(/message--tool-error/);

  await page.getByRole("button", { name: "Abort running Pi" }).click();
  await expectRunFinished(page);
  await expect(restored).toHaveCount(1);
});
