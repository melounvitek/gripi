import { expect, test } from "@playwright/test";
import { activeRecovery, prompts, replies } from "../support/contract.mjs";
import { expectRunFinished, message, sendPrompt } from "../support/ui.mjs";

const activeGroup = (page) => page.getByRole("region", { name: "Active now", exact: true });
const toolCard = (page, id) => page.locator(`article[data-tool-call-id="${id}"]`);

async function focusActivity(page) {
  const toggle = page.getByRole("switch", { name: "Show agent activity" });
  if (await toggle.getAttribute("aria-checked") !== "false") await toggle.click();
  await expect(toggle).toHaveAttribute("aria-checked", "false");
}

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await page.getByRole("button", { name: "New session", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "New session" });
  await dialog.getByRole("combobox", { name: "Project" }).click();
  await page.getByRole("option", { name: new RegExp(activeRecovery.project) }).click();
  await dialog.getByRole("button", { name: "Start session" }).click();
  await expect(page.getByRole("heading", { level: 1, name: "New session (pending first assistant response)" })).toBeVisible();
  // Persist the isolated session and seed completed activity that must stay out of Active now.
  await sendPrompt(page, prompts.newSession);
  await expect(message(page, "assistant", replies.newSession)).toBeVisible();
  await expectRunFinished(page);
});

test.afterEach(async ({ page }) => {
  const abort = page.getByRole("button", { name: "Abort running Pi" });
  if (await abort.isVisible()) await abort.click();
});

test("reload recovers ordinary tool cards without duplicating completed history", async ({ page }) => {
  await sendPrompt(page, activeRecovery.toolsPrompt);
  await focusActivity(page);
  await expect(activeGroup(page).locator(".message")).toHaveCount(4);
  await expect(toolCard(page, "recovery-bash")).toContainText("bash recovery still running");
  await page.reload();
  await focusActivity(page);
  const group = activeGroup(page);
  await expect(group).toBeVisible();
  await expect(group.locator(".message")).toHaveCount(4);
  await expect(group).toContainText("4 running");
  for (const call of activeRecovery.tools) {
    await expect(toolCard(page, call.id)).toHaveCount(1);
    await expect(group.locator(`[data-tool-call-id="${call.id}"]`)).toContainText(call.arguments.path || "bash recovery still running");
  }
  await expect(page.locator("article[data-tool-call-id]:visible")).toHaveCount(4);
  await page.getByRole("button", { name: "Abort running Pi" }).click();
  await expectRunFinished(page);
  await expect(group).toHaveCount(0);
  for (const call of activeRecovery.tools) await expect(toolCard(page, call.id)).toHaveCount(1);
  await page.getByRole("switch", { name: "Show agent activity" }).click();
  await expect(toolCard(page, "recovery-bash")).toContainText("bash recovery aborted");
});

test("reconnected ordinary tools clear on abort without duplicate cards", async ({ page, context }) => {
  await sendPrompt(page, activeRecovery.toolsPrompt);
  await focusActivity(page);
  const group = activeGroup(page);
  await expect(group.locator(".message")).toHaveCount(4);

  // Disconnect the real event poll, then let it reconnect without injecting any events.
  const disconnected = page.waitForEvent("requestfailed", { predicate: (request) => new URL(request.url()).pathname === "/events" });
  await context.setOffline(true);
  await disconnected;
  const reconnected = page.waitForResponse((response) => new URL(response.url()).pathname === "/events" && response.ok());
  await context.setOffline(false);
  await reconnected;
  await expect(group.locator(".message")).toHaveCount(4);
  for (const call of activeRecovery.tools) await expect(toolCard(page, call.id)).toHaveCount(1);

  await page.getByRole("button", { name: "Abort running Pi" }).click();
  await expectRunFinished(page);
  await expect(group).toHaveCount(0);
  for (const call of activeRecovery.tools) {
    await expect(toolCard(page, call.id)).toHaveCount(1);
    await expect(toolCard(page, call.id)).toBeHidden();
  }
  await page.reload();
  await focusActivity(page);
  await expect(group).toHaveCount(0);
  for (const call of activeRecovery.tools) await expect(toolCard(page, call.id)).toHaveCount(1);
});

test("normal tool completion removes Active now and stays collapsed after reload", async ({ page }) => {
  await focusActivity(page);
  const group = activeGroup(page);
  await sendPrompt(page, prompts.longCommand);
  await expect(group.locator("article[data-tool-call-id]")).toHaveCount(1);
  await expectRunFinished(page);
  await expect(group).toHaveCount(0);
  await expect(page.locator("article[data-tool-call-id]:visible")).toHaveCount(0);
  await page.reload();
  await focusActivity(page);
  await expect(group).toHaveCount(0);
  await expect(page.locator("article[data-tool-call-id]:visible")).toHaveCount(0);
});

for (const transport of ["cumulative", "delta"]) {
  for (const phase of ["thinking", "preparation"]) {
    test(`reload restores only current ${transport} ${phase} and abort clears it`, async ({ page }) => {
      await sendPrompt(page, activeRecovery[`${phase}Prompt`][transport]);
      const current = phase === "thinking" ? ".message--thinking" : ".message--tool-preparation";
      const text = phase === "thinking" ? activeRecovery.thinking : "Preparing tool call…";
      await expect(page.locator(current).filter({ hasText: text })).toBeVisible();
      await page.reload();
      await focusActivity(page);
      const group = activeGroup(page);
      await expect(group).toBeVisible();
      await expect(group.locator(".message")).toHaveCount(1);
      await expect(group.locator(current)).toContainText(text);
      await expect(group).not.toContainText(activeRecovery.previousThinking);
      await expect(group).not.toContainText(activeRecovery.text);
      await expect(message(page, "assistant", activeRecovery.text)).toBeVisible();
      await expect(page.locator(".message--thinking").filter({ hasText: activeRecovery.previousThinking })).toBeHidden();
      await expect(page.locator("article[data-tool-call-id]:visible")).toHaveCount(0);

      await page.getByRole("button", { name: "Abort running Pi" }).click();
      await expectRunFinished(page);
      await expect(group).toHaveCount(0);
      await page.reload();
      await focusActivity(page);
      await expect(group).toHaveCount(0);
      await expect(page.locator(".message--thinking:visible, .message--tool-preparation:visible")).toHaveCount(0);
    });
  }
}
