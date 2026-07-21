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

test("abort an active run", async ({ page }) => {
  await page.goto("/");
  await selectSession(page, sessions.controlsAbort);
  await sendPrompt(page, prompts.abortStart);

  const abort = page.getByRole("button", { name: "Abort running Pi" });
  await expect(abort).toBeVisible();
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "running");
  await abort.click();
  await expectRunFinished(page);
  await expect(message(page, "assistant", replies.aborted)).toHaveCount(0);
});
