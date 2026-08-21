import { expect, test } from "@playwright/test";
import { prompts, replies, sessions } from "../support/contract.mjs";
import { expectRunFinished, message, selectSession, sendPrompt } from "../support/ui.mjs";

test("steer an active run", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.controlsSteer);
  await sendPrompt(page, prompts.steerStart);
  await expect(page.getByRole("button", { name: "Abort running Pi" })).toBeVisible();

  await sendPrompt(page, prompts.steerMessage);
  await expect(message(page, "assistant", replies.steer)).toBeVisible();
  await expectRunFinished(page);
});

test("use slash commands while Pi is running", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.controlsSteer);
  await sendPrompt(page, prompts.steerStart);

  const abort = page.getByRole("button", { name: "Abort running Pi" });
  await expect(abort).toBeVisible();
  const composer = page.getByLabel("Message to Pi");
  await composer.fill("/");
  await expect(page.locator('.command[data-command-name="name"]')).toBeVisible();
  await expect(page.locator('.command[data-command-name="steer-template"]')).toBeVisible();

  await composer.fill("/name Active commands");
  await page.locator(".prompt-form").evaluate((form) => form.requestSubmit());
  await expect(page.getByRole("heading", { level: 1, name: "Active commands" })).toBeVisible();
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "running");
  await expect(abort).toBeVisible();

  const failActiveName = async (route) => {
    if (!route.request().postData()?.includes("Failed active name")) return route.continue();
    await route.fulfill({ status: 422, contentType: "application/json", body: JSON.stringify({ error: "Name rejected" }) });
  };
  await page.route("**/prompt", failActiveName);
  await composer.fill("/name Failed active name");
  await page.locator(".prompt-form").evaluate((form) => form.requestSubmit());
  await expect(composer).toHaveValue("/name Failed active name");
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "running");
  await expect(abort).toBeVisible();
  await page.unroute("**/prompt", failActiveName);

  await composer.fill("/model");
  await page.locator(".prompt-form").evaluate((form) => form.requestSubmit());
  const modelDialog = page.getByRole("dialog", { name: "Model & thinking" });
  await expect(modelDialog).toBeVisible();
  await modelDialog.getByRole("radio", { name: /Contract Model/ }).check();
  await modelDialog.getByRole("button", { name: "Apply" }).click();
  await expect(modelDialog).toBeHidden();
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "running");
  await expect(abort).toBeVisible();

  await composer.fill("/immediate-command");
  await page.locator(".prompt-form").evaluate((form) => form.requestSubmit());
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "running");
  await expect(abort).toBeVisible();

  await page.route("**/sessions/export", async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 500));
    await route.continue();
  });
  const downloadPromise = page.waitForEvent("download");
  await composer.fill("/export active-run");
  await page.locator(".prompt-form").evaluate((form) => form.requestSubmit());
  await expect(abort).toBeVisible();
  const download = await downloadPromise;
  expect(download.suggestedFilename()).toBe("active-run.html");
  await download.delete();
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "running");
  await expect(abort).toBeVisible();

  await composer.fill("/steer-template");
  await page.locator(".prompt-form").evaluate((form) => form.requestSubmit());
  await expect(message(page, "assistant", replies.steer)).toBeVisible();
  await expectRunFinished(page);

  await sendPrompt(page, "/name E2E Steer Desktop");
  await expect(page.getByRole("heading", { level: 1, name: sessions.controlsSteer })).toBeVisible();
});

test("queue a built-in-looking slash command as a follow-up", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.controlsFollowUp);
  await sendPrompt(page, prompts.followUpStart);
  await page.getByRole("button", { name: "More send options" }).click();
  await page.getByRole("button", { name: "Queue follow-up" }).click();

  const composer = page.getByLabel("Message to Pi");
  await composer.fill("/logout");
  await page.locator(".prompt-form").evaluate((form) => form.requestSubmit());
  await expect(message(page, "assistant", replies.followUp)).toBeVisible();
  await expect(message(page, "gateway", "restart the Gripi gateway")).toHaveCount(0);
  await expectRunFinished(page);
});

test("queue a follow-up for an active run", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.controlsFollowUp);
  await sendPrompt(page, prompts.followUpStart);
  await expect(page.getByRole("button", { name: "Abort running Pi" })).toBeVisible();

  await page.getByRole("button", { name: "More send options" }).click();
  await page.getByRole("button", { name: "Queue follow-up" }).click();
  await sendPrompt(page, prompts.followUpMessage);
  await expect(message(page, "assistant", replies.followUp)).toBeVisible();
  await expectRunFinished(page);
});

test("runs extension commands and queues steering during compaction", async ({ page, context }) => {
  await page.goto("/");
  await selectSession(page, sessions.compactionFollowUp);

  await page.getByLabel("Message to Pi").fill("/compact");
  await page.locator(".prompt-form").evaluate((form) => form.requestSubmit());
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "running");
  await expect(page.locator(".composer-state")).toContainText("Compacting…");

  const composer = page.getByLabel("Message to Pi");
  const immediateResponsePromise = page.waitForResponse((response) => response.request().postData()?.includes("/immediate-command"));
  await composer.fill("/immediate-command");
  await page.locator(".prompt-form").evaluate((form) => form.requestSubmit());
  const immediateResponse = await immediateResponsePromise;
  expect(immediateResponse.status()).toBe(200);
  expect(await immediateResponse.json()).toMatchObject({ compacting: true, running: false });
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "running");
  await expect(page.locator(".composer-state")).toContainText("Compacting…");

  await sendPrompt(page, prompts.standard);
  const queuedSteer = page.locator(".pending-message--steering");
  await expect(queuedSteer).toHaveText(`Steering: ${prompts.standard}`);
  await expect(page.locator('[data-pending-compaction="true"]')).toBeVisible();

  const restoredPage = await context.newPage();
  await restoredPage.goto("/");
  await selectSession(restoredPage, sessions.compactionFollowUp);
  await expect(restoredPage.locator(".pending-message--steering")).toHaveText(`Steering: ${prompts.standard}`);
  await restoredPage.close();

  await expect(message(page, "assistant", replies.standard)).toBeVisible();
  await expect(queuedSteer).toHaveCount(0);
  await expectRunFinished(page);
});

test("abort an active run before navigating the session tree", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.controlsAbort);
  await sendPrompt(page, prompts.steerStart);
  await expect(page.getByRole("button", { name: "Abort running Pi" })).toBeVisible();

  const composer = page.getByLabel("Message to Pi");
  await composer.fill("/tree");
  await page.locator(".prompt-form").evaluate((form) => form.requestSubmit());
  const dialog = page.getByRole("dialog", { name: "Session tree" });
  await expect(dialog).toBeVisible();
  const firstEntry = dialog.locator("[data-tree-viewport] > [role=treeitem] > .tree-session-row [data-tree-entry-id]");
  await firstEntry.click();
  await dialog.locator("[data-tree-navigate]").click();
  await dialog.locator("[data-tree-summary-submit]").click();

  await expect(dialog).toBeHidden();
  await expect(page.getByRole("button", { name: "Abort running Pi" })).toBeHidden();
});

test("abort an active run before compacting", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.controlsAbort);
  await sendPrompt(page, prompts.steerStart);
  await expect(page.getByRole("button", { name: "Abort running Pi" })).toBeVisible();

  const composer = page.getByLabel("Message to Pi");
  await composer.fill("/compact");
  await page.locator(".prompt-form").evaluate((form) => form.requestSubmit());
  await expect(page.locator('[data-pending-compaction="true"]')).toBeVisible();
  await expectRunFinished(page);
});

test("shows login guidance without steering an active run", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.controlsAbort);
  await sendPrompt(page, prompts.steerStart);

  const abort = page.getByRole("button", { name: "Abort running Pi" });
  await expect(abort).toBeVisible();
  await sendPrompt(page, "/login anthropic");
  await expect(message(page, "gateway", "restart the Gripi gateway")).toBeVisible();
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "running");
  await expect(abort).toBeVisible();
  await abort.click();
  await expectRunFinished(page);

  await page.route("**/prompt", async (route) => {
    if (route.request().postData()?.includes("/login xai")) await new Promise((resolve) => setTimeout(resolve, 800));
    await route.continue();
  });
  await sendPrompt(page, prompts.standard);
  await expect(abort).toBeVisible();
  const delayedGuidance = page.waitForResponse((response) => response.request().postData()?.includes("/login xai"));
  await sendPrompt(page, "/login xai");
  await expect(abort).toBeHidden();
  await delayedGuidance;
  await expectRunFinished(page);
});

test("keep sidebar metadata refreshes fast while an active run is deferred", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.controlsSteer);
  await sendPrompt(page, prompts.steerStart);
  const abort = page.getByRole("button", { name: "Abort running Pi" });
  await expect(abort).toBeVisible();

  const sidebar = page.locator(".session-sidebar");
  await expect(sidebar).toHaveAttribute("data-sidebar-metadata-deferred", "");
  await abort.click();
  await expectRunFinished(page);
  await expect(sidebar).not.toHaveAttribute("data-sidebar-metadata-deferred", "");
});

test("mark a final reply read without a sidebar refresh", async ({ page }) => {
  await page.goto("/");
  const otherSessionUrl = page.url();
  await selectSession(page, sessions.markRead);
  const sessionOnlyUrl = new URL(page.url());
  sessionOnlyUrl.searchParams.set("session_only", "1");
  await page.goto(sessionOnlyUrl.toString());

  const liveOutput = page.locator("#live-output");
  const initialCount = Number(await liveOutput.getAttribute("data-assistant-response-count"));
  const markReadResponse = page.waitForResponse((response) => new URL(response.url()).pathname === "/sessions/mark_read");
  await sendPrompt(page, prompts.standard);
  await expect(message(page, "assistant", replies.standard)).toBeVisible();
  await expectRunFinished(page);

  const response = await markReadResponse;
  expect(response.status()).toBe(204);
  const body = new URLSearchParams(response.request().postData());
  expect(body.get("assistant_response_count")).toBe(String(initialCount + 1));
  await expect(liveOutput).toHaveAttribute("data-assistant-response-count", String(initialCount + 1));

  await page.goto(otherSessionUrl);
  const sessionLink = page.locator("a.session", { hasText: sessions.markRead });
  await expect(sessionLink).not.toHaveClass(/unread/);
});

test("show an active run in the sidebar and abort it", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.controlsAbort);
  await sendPrompt(page, prompts.abortStart);

  const activeSession = page.locator("a.session", { hasText: sessions.controlsAbort });
  await expect(activeSession.locator(".session-running-indicator")).toBeVisible();
  await selectSession(page, sessions.marker);
  await expect(activeSession.locator(".session-running-indicator")).toBeVisible();
  await selectSession(page, sessions.controlsAbort);

  const abort = page.getByRole("button", { name: "Abort running Pi" });
  await expect(abort).toBeVisible();
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "running");
  await abort.click();
  await expectRunFinished(page);
  await expect(activeSession.locator(".session-running-indicator")).toHaveCount(0);
  await expect(message(page, "assistant", replies.aborted)).toHaveCount(0);
});
