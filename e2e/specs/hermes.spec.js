import { mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { expect, test } from "@playwright/test";
import { hermes, sessions } from "../support/contract.mjs";
import { expectRunFinished, liveMessage, message, selectSession, sendPrompt } from "../support/ui.mjs";

// Each test talks in a new conversation, on the page that the Hermes panel shows.
test.beforeEach(async ({ page }) => {
  expect((await page.request.post("/hermes/new")).ok()).toBe(true);
  await page.goto("/hermes");
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

test("a new session starts Pi in the first project, as without Hermes", async ({ page }) => {
  await page.goto("/");
  await page.keyboard.press("Control+n");
  const dialog = page.getByRole("dialog", { name: "New session", exact: true });
  await expect(dialog.getByRole("combobox", { name: "Project or path" })).toBeFocused();
  await expect(dialog.getByRole("option").first()).toHaveAttribute("aria-selected", "true");

  await page.keyboard.press("Enter");
  await expect(page.getByRole("heading", { level: 1, name: "New session (pending first assistant response)" })).toBeVisible();
  await expect(page.getByLabel("Message to Pi")).toBeVisible();
  // Leftover sessions push seeded ones off the first sidebar page for later tests.
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

  // Finding what was said opens its transcript.
  await page.keyboard.press("Control+f");
  await page.getByRole("searchbox", { name: "Find in conversation" }).fill(transcript);
  await expect(spoken.locator(".message-body")).toBeVisible();
});

test("talking over Hermes's work in a voice call stops it and sends what was said", async ({ page }) => {
  await sendPrompt(page, hermes.longPrompt);
  await expect(page.getByRole("button", { name: "Abort running Hermes" })).toBeVisible();

  // The fake microphone talks while Hermes works on a prompt that holds until it's stopped.
  await page.getByRole("button", { name: "Start a voice conversation" }).click();
  await expect(liveMessage(page, "user", hermes.voice.transcript)).toHaveCount(1);
  await expect(page.locator("[data-voice-call-view] [data-voice-call-state]")).toHaveText("speaking");

  await page.getByRole("group", { name: "Voice conversation with Hermes" }).getByRole("button", { name: "end", exact: true }).click();
  await expectRunFinished(page);
});

test("a voice call keeps its session from notifying while the page is away", async ({ page }) => {
  await page.addInitScript(() => {
    window.replyNotifications = [];
    document.hasFocus = () => false;
    localStorage.removeItem("gripi:notifications-disabled");
    window.gripiElectron = { showNotification: async (notification) => window.replyNotifications.push(notification) };
  });
  const presence = [];
  page.on("request", (request) => {
    if (request.url().endsWith("/web-push/presence")) presence.push(request.postDataJSON());
  });
  await page.reload();
  await page.getByRole("button", { name: "Start a voice conversation" }).click();
  await expect.poll(() => presence.at(-1)?.focused).toBe(true);

  const state = page.locator("[data-voice-call-view] [data-voice-call-state]");
  await expect(state).toHaveText("speaking");
  await expect(state).toHaveText("listening");
  expect(await page.evaluate(() => window.replyNotifications)).toEqual([]);

  await page.getByRole("group", { name: "Voice conversation with Hermes" }).getByRole("button", { name: "end", exact: true }).click();
  await expect.poll(() => presence.at(-1)?.focused).toBe(false);
});

test("a reply notification opens the current conversation, which a new one may have replaced", async ({ page }) => {
  await page.addInitScript(() => {
    window.replyNotifications = [];
    document.hasFocus = () => false;
    localStorage.removeItem("gripi:notifications-disabled");
    window.gripiElectron = { showNotification: async (notification) => window.replyNotifications.push(notification) };
  });
  await page.reload();
  await sendPrompt(page, "List the files");
  await expect.poll(() => page.evaluate(() => window.replyNotifications.map(({ title, url }) => ({ title, url })))).toEqual([{ title: "Hermes", url: "/hermes" }]);
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

test("only Hermes's conversation offers a voice conversation", async ({ page }) => {
  await expect(page.getByRole("button", { name: "Start a voice conversation" })).toBeVisible();
  await page.goto("/");
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
