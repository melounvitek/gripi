import { expect, test as base } from "@playwright/test";
import { activeRecovery, prompts, replies } from "../support/contract.mjs";
import { activityView, expectRunFinished, message, sendPrompt, startSession } from "../support/ui.mjs";

const activeGroup = (page) => page.getByRole("region", { name: "Active now", exact: true });
const toolCard = (page, id) => page.locator(`article[data-tool-call-id="${id}"]`);
const toolCount = activeRecovery.tools.length;

async function focusActivity(page) {
  const brief = activityView(page, "Brief");
  await brief.click();
  await expect(brief).toHaveAttribute("aria-pressed", "true");
}

const test = base.extend({
  recoverySession: [async ({ page }, use) => {
    await page.goto("/");
    await startSession(page, activeRecovery.project);
    await expect(page.getByRole("heading", { level: 1, name: "New session (pending first assistant response)" })).toBeVisible();
    // Persist the isolated session and seed completed activity that must stay out of Active now.
    await sendPrompt(page, prompts.newSession);
    await expect(message(page, "assistant", replies.newSession)).toBeVisible();
    await expectRunFinished(page);
    const session = new URL(page.url()).searchParams.get("session");
    try {
      await use(session);
    } finally {
      const abort = page.getByRole("button", { name: "Abort running Pi" });
      if (await abort.isVisible()) {
        await abort.click();
        await expectRunFinished(page);
      }
      const deleted = await page.request.post("/sessions/delete", { form: { session } });
      expect(deleted.ok()).toBe(true);
    }
  }, { auto: true }],
});

test("reload recovers ordinary tool cards without duplicating completed history", async ({ page }) => {
  await sendPrompt(page, activeRecovery.toolsPrompt);
  await focusActivity(page);
  await expect(activeGroup(page).locator(".message")).toHaveCount(toolCount);
  await expect(toolCard(page, "recovery-bash")).toContainText("bash recovery still running");
  const editLines = ["Edit 1", "- before", "- second line", "+ after", "+ replacement line"];
  await expect(toolCard(page, "recovery-edit").locator(".message-body .tool-diff-line")).toHaveText(editLines);
  await page.reload();
  await focusActivity(page);
  const group = activeGroup(page);
  await expect(group).toBeVisible();
  await expect(group.locator(".message")).toHaveCount(toolCount);
  await expect(group).toContainText(`${toolCount} running`);
  const shortWrite = toolCard(page, "recovery-write");
  await expect(shortWrite.locator(".tool-diff-line")).toHaveText(["+ Recovery content", "+ Second line"]);
  const longWrite = toolCard(page, "recovery-write-long");
  await longWrite.getByRole("button", { name: "Expand", exact: true }).click();
  await expect(longWrite.locator(".message-body .tool-diff-line")).toHaveCount(40);
  await expect(toolCard(page, "recovery-edit").locator(".message-body .tool-diff-line")).toHaveText(editLines);
  for (const call of activeRecovery.tools) {
    await expect(toolCard(page, call.id)).toHaveCount(1);
    await expect(group.locator(`[data-tool-call-id="${call.id}"]`)).toContainText(call.arguments.path || "bash recovery still running");
  }
  await expect(page.locator("article[data-tool-call-id]:visible")).toHaveCount(toolCount);
  await page.getByRole("button", { name: "Abort running Pi" }).click();
  await expectRunFinished(page);
  await expect(group).toHaveCount(0);
  for (const call of activeRecovery.tools) await expect(toolCard(page, call.id)).toHaveCount(1);
  await activityView(page, "Full").click();
  await expect(toolCard(page, "recovery-bash")).toContainText("bash recovery aborted");
  await expect(shortWrite.locator(".message-body")).toContainText("+ Recovery content");
  await expect(longWrite.locator(".message-body")).toContainText("+ Recovery line 1");
  await expect(longWrite.locator(".message-body")).toContainText("+ Recovery line 40");
});

test("reconnected ordinary tools clear on abort without duplicate cards", async ({ page, context }) => {
  await sendPrompt(page, activeRecovery.toolsPrompt);
  await focusActivity(page);
  const group = activeGroup(page);
  await expect(group.locator(".message")).toHaveCount(toolCount);

  // Disconnect the real event poll, then let it reconnect without injecting any events.
  const disconnected = page.waitForEvent("requestfailed", { predicate: (request) => new URL(request.url()).pathname === "/events" });
  await context.setOffline(true);
  await disconnected;
  const reconnected = page.waitForResponse((response) => new URL(response.url()).pathname === "/events" && response.ok());
  await context.setOffline(false);
  await reconnected;
  await expect(group.locator(".message")).toHaveCount(toolCount);
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

test("snapshot reconciliation retains expanded running cards and their original positions", async ({ page }) => {
  await sendPrompt(page, activeRecovery.toolsPrompt);
  await expect(toolCard(page, "recovery-bash")).toContainText("bash recovery still running");
  await page.reload();
  await focusActivity(page);
  const group = activeGroup(page);
  const write = toolCard(page, "recovery-write-long");
  await write.getByRole("button", { name: "Expand", exact: true }).click();
  await write.evaluate((card) => { window.retainedActivityCard = card; });
  let refresh = true;
  await page.route(/\/events(?:\?|$)/, async (route) => {
    const response = await route.fetch();
    const payload = await response.json();
    if (refresh) {
      refresh = false;
      // Conflict recovery uses the in-place snapshot reconciler, not page navigation.
      await route.fulfill({ response, json: { ...payload, missed: true, session_sync: { mode: "conflict" } } });
    } else await route.fulfill({ response, json: payload });
  });
  await page.waitForResponse(/\/session_fragment(?:\?|$)/);
  await expect(group.locator(".message")).toHaveCount(toolCount);
  await expect.poll(() => write.evaluate((card) => card === window.retainedActivityCard)).toBe(true);
  await expect(write.locator('[data-tool-output-collapse]')).toHaveAttribute("data-expanded", "true");
  await activityView(page, "Full").click();
  await expect(page.locator('#conversation-scroll > article[data-tool-call-id="recovery-write-long"]')).toHaveCount(1);
  await focusActivity(page);
  // An authoritative snapshot with no active tools must clear retained activity flags,
  // even when a crashed tool left its persisted call unchanged.
  await page.route(/\/session_fragment(?:\?|$)/, async (route) => {
    const response = await route.fetch();
    const payload = await response.json();
    payload.conversation_html = payload.conversation_html.replace(/data-active-tool-events="[^"]*"/, 'data-active-tool-events="[]"');
    await route.fulfill({ response, json: payload });
  });
  refresh = true;
  await page.waitForResponse(/\/session_fragment(?:\?|$)/);
  await expect(write).not.toHaveAttribute("data-activity-active", "");
  await expect(group).toContainText("Pi is working…");
  await expect(group.locator(".message")).toHaveCount(0);
  await expect(group).not.toContainText("Done");
  await expect(write).toHaveCount(1);
  await expect(write).toBeHidden();
  // An idle snapshot must retire the shell, not leave a permanent waiting state.
  await page.route(/\/session_fragment(?:\?|$)/, async (route) => {
    const response = await route.fetch();
    const payload = await response.json();
    payload.conversation_html = payload.conversation_html
      .replace(/data-active-tool-events="[^"]*"/, 'data-active-tool-events="[]"')
      .replace(/data-agent-running="[^"]*"/, 'data-agent-running="false"');
    await route.fulfill({ response, json: payload });
  });
  refresh = true;
  await page.waitForResponse(/\/session_fragment(?:\?|$)/);
  await expect(group).toHaveCount(0);
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
      // Exact text: the unfinished thinking after this paragraph must stay hidden.
      await expect(page.locator(current).filter({ hasText: text }).locator(".message-body")).toHaveText(text);
      await page.reload();
      await focusActivity(page);
      const group = activeGroup(page);
      await expect(group).toBeVisible();
      await expect(group.locator(".message")).toHaveCount(1);
      await expect(group.locator(`${current} .message-body`)).toHaveText(text);
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
