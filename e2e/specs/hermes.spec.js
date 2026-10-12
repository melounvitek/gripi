import { mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
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

test("an attached image reaches Hermes and stays after a reload", async ({ page }) => {
  const prompt = "Describe the attached image";
  const chooser = page.waitForEvent("filechooser");
  await page.locator(".attach-button").click();
  await (await chooser).setFiles({ name: "hermes-image.png", mimeType: "image/png", buffer: await page.screenshot() });
  await sendPrompt(page, prompt);

  await expect(liveMessage(page, "assistant", `${hermes.run.replyPrefix}${prompt}`)).toBeVisible();
  await expect(liveMessage(page, "user", prompt).locator(".message-image")).toBeVisible();
  await expectRunFinished(page);

  await page.reload();
  await expect(message(page, "user", prompt).locator(".message-image")).toBeVisible();
});

test("an image Hermes shares shows in its reply and stays after a reload", async ({ page }) => {
  const image = join(await mkdtemp(join(tmpdir(), "gripi-hermes-share-")), "shot.png");
  await writeFile(image, await page.screenshot());
  // The fake Hermes repeats the prompt in its reply.
  const prompt = "Here is the screenshot:";
  await sendPrompt(page, `${prompt} MEDIA:${image}`);

  const reply = `${hermes.run.replyPrefix}${prompt}`;
  await expect(liveMessage(page, "assistant", reply).locator(".message-image")).toBeVisible();
  await expect(liveMessage(page, "assistant", reply)).not.toContainText("MEDIA:");
  await expectRunFinished(page);

  await page.reload();
  const saved = message(page, "assistant", reply);
  await expect(saved.locator(".message-image")).toBeVisible();
  await expect(saved.locator(".message-image")).toHaveJSProperty("complete", true);
  expect(await saved.locator(".message-image").evaluate((element) => element.naturalWidth)).toBeGreaterThan(0);
});

test("stopping Hermes ends its run", async ({ page }) => {
  await sendPrompt(page, hermes.stopPrompt);
  // A finished paragraph shows while Hermes still works.
  await expect(page.locator("#live-output article.message--thinking").filter({ hasText: hermes.stopThought })).toBeVisible();
  // Hermes would take another message as a correction of its turn, which Gripi doesn't offer: only Stop is left.
  const composer = page.getByLabel("Message to Hermes");
  await expect(composer).toBeDisabled();
  await expect(composer).toHaveAttribute("placeholder", "Hermes is working…");
  await expect(page.getByRole("button", { name: /^(Send|Steer)/ })).toBeHidden();
  await expect(page.locator("#image-input")).toBeDisabled();
  await page.getByRole("button", { name: "Abort running Hermes" }).click();
  await expectRunFinished(page);
  await expect(composer).toHaveAttribute("placeholder", "Ask Hermes…");
  await expect(page.locator("#image-input")).toBeEnabled();
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

test("the agent filter shows only Hermes's sessions, each marked as Hermes's", async ({ page }) => {
  await page.getByRole("combobox", { name: "Filter sessions by agent" }).click();
  await page.getByRole("option", { name: "hermes" }).click();
  const rows = page.locator(".sessions-list .session-row, .current-session-section .session-row");
  await expect(rows.first()).toBeVisible();
  await expect(page.locator('.session-row[data-agent="pi"]')).toHaveCount(0);
  await expect(page.locator(".session-row").filter({ hasText: hermes.saved.title }).locator('.session-agent[data-agent="hermes"]')).toBeVisible();
});

test("a new session starts Pi in the first project, as without Hermes", async ({ page }) => {
  await page.keyboard.press("Control+n");
  const dialog = page.getByRole("dialog", { name: "New session", exact: true });
  await expect(dialog.getByRole("combobox", { name: "Project or path" })).toBeFocused();
  await expect(dialog.getByRole("option").first()).toHaveAttribute("aria-selected", "true");
  await expect(dialog.getByRole("option", { name: /^hermes\s*Hermes Agent$/ })).toBeVisible();

  await page.keyboard.press("Enter");
  await expect(page.getByRole("heading", { level: 1, name: "New session (pending first assistant response)" })).toBeVisible();
  await expect(page.locator(".session-header-agent")).toHaveAttribute("data-agent", "pi");
  // Leftover sessions push seeded ones off the first sidebar page for later tests.
  const deleted = await page.request.post("/sessions/delete", { form: { session: new URL(page.url()).searchParams.get("session") } });
  expect(deleted.ok()).toBe(true);
});

test("typing its name in the new session dialog starts Hermes", async ({ page }) => {
  await page.keyboard.press("Control+n");
  const input = page.getByRole("dialog", { name: "New session", exact: true }).getByRole("combobox", { name: "Project or path" });
  await input.fill("hermes");
  await input.press("Enter");
  await expect(page.getByRole("heading", { level: 1, name: "New session (pending first assistant response)" })).toBeVisible();
  await expect(page.locator(".session-header-agent")).toHaveText("hermes");
});

test("a project picked after Hermes failed to start starts Pi", async ({ page }) => {
  let failed = false;
  await page.route("**/sessions/new_at_cwd", (route) => {
    if (failed) return route.continue();
    failed = true;
    return route.fulfill({ status: 502, json: { error: "Hermes could not start" } });
  });
  await page.keyboard.press("Control+n");
  const dialog = page.getByRole("dialog", { name: "New session", exact: true });
  await dialog.getByRole("option", { name: /^hermes\s*Hermes Agent$/ }).click();
  await expect(dialog).toContainText("Hermes could not start");

  await dialog.locator("[data-new-session-project]").first().click();
  await expect(page.getByRole("heading", { level: 1, name: "New session (pending first assistant response)" })).toBeVisible();
  await expect(page.locator(".session-header-agent")).toHaveAttribute("data-agent", "pi");
  const deleted = await page.request.post("/sessions/delete", { form: { session: new URL(page.url()).searchParams.get("session") } });
  expect(deleted.ok()).toBe(true);
});

test("Hermes reads its own / and ! commands", async ({ page }) => {
  for (const command of ["/export", "!ls"]) {
    await sendPrompt(page, command);
    await expect(liveMessage(page, "assistant", `${hermes.run.replyPrefix}${command}`)).toBeVisible();
    await expectRunFinished(page);
  }
});
