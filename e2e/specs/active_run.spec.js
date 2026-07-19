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
