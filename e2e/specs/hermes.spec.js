import { mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { expect, test } from "@playwright/test";
import { hermes, sessions } from "../support/contract.mjs";
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

test("the agent filter stays after switching to another session", async ({ page }) => {
  await page.getByRole("combobox", { name: "Filter sessions by agent" }).click();
  await page.getByRole("option", { name: "pi" }).click();
  await expect(page.locator('.sessions-list .session-row[data-agent="hermes"]')).toHaveCount(0);
  await page.locator('.sessions-list .session-row[data-agent="pi"] a.session').first().click();
  await expect(page.locator(".session-header-agent")).toHaveAttribute("data-agent", "pi");

  // The sidebar's refreshes read the filters from the address, as a reload does.
  await page.reload();
  await expect(page.locator('select[name="agent"]')).toHaveValue("pi");
  await expect(page.locator('.session-row[data-agent="hermes"]')).toHaveCount(0);
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

test("a new Pi session keeps the agent filter for Pi", async ({ page }) => {
  await page.getByRole("combobox", { name: "Filter sessions by agent" }).click();
  await page.getByRole("option", { name: "pi" }).click();
  await expect(page.locator('.sessions-list .session-row[data-agent="hermes"]')).toHaveCount(0);
  await page.keyboard.press("Control+n");
  await page.keyboard.press("Enter");

  await expect(page.locator(".session-header-agent")).toHaveAttribute("data-agent", "pi");
  await expect(page.locator('select[name="agent"]')).toHaveValue("pi");
  const deleted = await page.request.post("/sessions/delete", { form: { session: new URL(page.url()).searchParams.get("session") } });
  expect(deleted.ok()).toBe(true);
});

test("a new Hermes session drops the agent filter for Pi, which would hide it", async ({ page }) => {
  await page.getByRole("combobox", { name: "Filter sessions by agent" }).click();
  await page.getByRole("option", { name: "pi" }).click();
  await expect(page.locator('.sessions-list .session-row[data-agent="hermes"]')).toHaveCount(0);
  await page.keyboard.press("Control+n");
  const input = page.getByRole("dialog", { name: "New session", exact: true }).getByRole("combobox", { name: "Project or path" });
  await input.fill("hermes");
  await input.press("Enter");

  await expect(page.locator(".session-header-agent")).toHaveAttribute("data-agent", "hermes");
  await expect(page.locator('select[name="agent"]')).toHaveValue("");
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

test("a voice conversation takes the conversation's place, sends what was said and speaks Hermes's reply", async ({ page }) => {
  const { transcript } = hermes.voice;
  const speech = page.waitForResponse((response) => response.url().endsWith("/voice/speak") && response.ok());
  await page.getByRole("button", { name: "Start a voice conversation" }).click();
  const call = page.getByRole("group", { name: "Voice conversation with Hermes" });
  const view = page.locator("[data-voice-call-view]");
  const state = view.locator("[data-voice-call-state]");
  await expect(state).toHaveText("listening");
  await expect(page.getByLabel("Message to Hermes")).toBeHidden();
  expect(await conversationCovered(page)).toBe(true);

  // The fake microphone says something for a second, and the fake dashboard hears the transcript.
  const spoken = message(page, "user", transcript).last();
  await expect(spoken.locator(".message-body")).toHaveText(transcript);
  await expect(spoken.locator(".message-voice-mark")).toHaveText("voice");
  expect(new URLSearchParams((await speech).request().postData()).get("text")).toContain(hermes.run.replyPrefix.trim());
  await expect(state).toHaveText("speaking");
  await expect(view.locator("[data-voice-call-sentence]")).toContainText(hermes.run.replyPrefix.trim());
  await expect(state).toHaveText("listening");
  await expect(view.locator("[data-voice-call-sentence]")).toBeEmpty();

  await call.getByRole("button", { name: "end", exact: true }).click();
  await expect(call).toBeHidden();
  await expect(view).toBeHidden();
  await expect(page.getByLabel("Message to Hermes")).toBeVisible();
  expect(await conversationCovered(page)).toBe(false);

  // What was said shows as bars, which open to its transcript.
  const bars = spoken.locator(".message-spoken-bars");
  await expect(bars).toHaveText(/^[▁-█ ]+$/);
  await expect(spoken.locator(".message-body")).toBeHidden();
  const liveBars = await bars.textContent();
  await bars.click();
  await expect(spoken.locator(".message-body")).toBeVisible();

  await page.reload();
  await expect(spoken.locator(".message-body")).toHaveText(transcript);
  await expect(spoken.locator(".message-voice-mark")).toHaveText("voice");
  await expect(bars).toHaveText(liveBars);
  await expect(spoken.locator(".message-body")).toBeHidden();
});

// Whether something other than the conversation shows at its centre.
function conversationCovered(page) {
  return page.evaluate(() => {
    const conversation = document.querySelector("#conversation-scroll");
    const box = conversation.getBoundingClientRect();
    return !conversation.contains(document.elementFromPoint(box.x + box.width / 2, box.y + box.height / 2));
  });
}

test("a refused microphone ends the call and the composer says why", async ({ page }) => {
  await page.evaluate(() => {
    navigator.mediaDevices.getUserMedia = () => Promise.reject(new DOMException("Permission denied", "NotAllowedError"));
  });
  await page.getByRole("button", { name: "Start a voice conversation" }).click();
  await expect(page.locator(".composer-state")).toHaveText("Microphone access was denied");
  await expect(page.getByLabel("Message to Hermes")).toBeVisible();
});

test("a refresh that skipped a voice call's events ends its wait with the reply to the awaited message", async ({ page }) => {
  const spoken = await page.evaluate(async () => {
    const { VoiceCallController } = await import("/assets/voice_call_controller.js");
    let messages = [];
    const controller = new VoiceCallController(window, { messages: () => messages });
    const replies = [];
    controller.conversation = { state: "thinking", awaited: "Run the backup", replyFinished: (text) => replies.push(text) };
    const message = (role, html, final = false) => {
      const article = document.createElement("article");
      article.dataset.role = role;
      if (final) article.dataset.finalAssistantResponse = "true";
      article.innerHTML = `<div class="message-body">${html}</div>`;
      return article;
    };
    const finished = () => {
      controller.replyFinishedOnPage();
      return replies.splice(0);
    };

    // A page from before Hermes took the message shows the turn before, also to a call that joined a running turn.
    messages = [message("user", "Check the disks"), message("assistant", "<p>The disks are fine.</p>", true)];
    const earlier = finished();
    // A turn that ended without a reply, such as a stopped one, leaves nothing to say.
    messages = [message("user", "Run the backup"), message("toolResult", "backup: interrupted")];
    const stopped = finished();
    messages = [message("user", "Run the backup"), message("assistant", "<p>The backup finished.</p><blockquote><p>Run it again with</p>" +
      '<div class="message-code-block"><pre>npm run backup</pre><button class="code-block-copy-button">Copy</button></div>' +
      "<p>It takes ten minutes.</p></blockquote><table><tr><td>photos</td></tr></table>", true)];
    return { earlier, stopped, answered: finished().map((text) => text.replace(/\s+/g, " ").trim()) };
  });

  expect(spoken).toEqual({ earlier: [], stopped: [""], answered: ["The backup finished. Run it again with It takes ten minutes."] });
});

test("only Hermes sessions offer a voice conversation", async ({ page }) => {
  await expect(page.getByRole("button", { name: "Start a voice conversation" })).toBeVisible();
  await selectSession(page, sessions.marker);
  await expect(page.getByLabel("Message to Pi")).toBeVisible();
  await expect(page.getByRole("button", { name: "Start a voice conversation" })).toHaveCount(0);
});

test("Hermes reads its own / and ! commands", async ({ page }) => {
  for (const command of ["/export", "!ls"]) {
    await sendPrompt(page, command);
    await expect(liveMessage(page, "assistant", `${hermes.run.replyPrefix}${command}`)).toBeVisible();
    await expectRunFinished(page);
  }
});
