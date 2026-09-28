import { expect, test } from "@playwright/test";
import { mobileSubagents, prompts, sessions, subagents } from "../support/contract.mjs";
import { expectRunFinished, sendPrompt } from "../support/ui.mjs";

test.use({ hasTouch: true });

async function liveEvents(page) {
  const events = [];
  await page.route(/\/events(?:\?|$)/, async (route) => {
    const after = Number(new URL(route.request().url()).searchParams.get("after") || 0);
    await route.fulfill({ json: { events: events.slice(after), last_seq: events.length, missed: false } });
  });
  await page.goto(`/?session_search=${encodeURIComponent(sessions.marker)}`);
  await page.goto(await page.getByRole("link", { name: new RegExp(sessions.marker) }).getAttribute("href"));
  await page.getByRole("switch", { name: "Show agent activity" }).tap();
  return async (...updates) => {
    events.push(...updates);
    await page.evaluate(() => window.dispatchEvent(new Event("pageshow")));
  };
}

const assistant = (content) => ({ role: "assistant", content });
const call = (id, name, args) => ({ type: "toolCall", id, name, arguments: args });
const start = (id, name, args) => ({ type: "tool_execution_start", toolCallId: id, toolName: name, args });
const end = (id, name, text = "Finished", isError = false) => ({ type: "tool_execution_end", toolCallId: id, toolName: name, result: { content: [{ type: "text", text }] }, isError });
const activeGroup = (page) => page.getByRole("region", { name: "Active now", exact: true });

test("bridges activity gaps without removing the group or keeping a stale running count", async ({ page }) => {
  const deliver = await liveEvents(page);
  const group = activeGroup(page);
  await deliver({ type: "agent_start" });
  await expect(group).toHaveCount(0);
  await deliver(start("gap-a", "bash", { command: "inspect database" }));
  await expect(group).toContainText("1 running");
  await group.evaluate((element) => { window.activityShell = element; });
  await deliver(end("gap-a", "bash", "Database inspected"), { type: "turn_end" });
  await expect(group.locator(".active-activity-count")).toHaveText("Done");
  await expect(group).toContainText("Database inspected");
  await expect(group.locator("[data-activity-active]")).toHaveCount(0);
  await deliver({ type: "turn_start" }, start("gap-b", "read", { path: "schema.sql" }));
  await expect(group).toContainText("1 running");
  await expect(group.locator('[data-tool-call-id="gap-b"]')).toBeVisible();
  await expect(page.locator('[data-tool-call-id="gap-a"]')).toBeHidden();
  // The previous completion timer must not retire the new activity.
  await page.waitForTimeout(1600);
  await expect(group).toContainText("1 running");
  await deliver(end("gap-b", "read"));
  await expect(group.locator(".active-activity-count")).toHaveText("Done");
  await expect(group).toContainText("Pi is working…");
  await expect(group.locator(".message")).toHaveCount(0);
  expect(await group.evaluate((element) => element === window.activityShell)).toBe(true);
  await page.screenshot({ path: test.info().outputPath("activity-gap.png") });
  await deliver(start("gap-c", "bash", { command: "verify schema" }));
  await expect(group).toContainText("1 running");
  await deliver({ type: "agent_end" });
  await expect(group).toHaveCount(0);
});

test("activity height settles after a shorter replacement and timers cannot recreate a hidden group", async ({ page }) => {
  const deliver = await liveEvents(page);
  const group = activeGroup(page);
  const output = Array.from({ length: 10 }, () => "Long output line").join("\n");
  await deliver({ type: "agent_start" }, start("tall", "bash", { command: "inspect" }), { type: "tool_execution_update", toolCallId: "tall", toolName: "bash", partialResult: { content: [{ type: "text", text: output }] } });
  await expect(group).toContainText("Long output line");
  const tallHeight = (await group.boundingBox()).height;
  await group.evaluate((element) => {
    const observer = new MutationObserver(() => {
      if (!element.querySelector('[data-tool-call-id="short"]')) return;
      window.activityReplacementHeight = element.getBoundingClientRect().height;
      observer.disconnect();
    });
    observer.observe(element, { childList: true });
    window.activityShrinkHeights = [];
    element.addEventListener("transitionrun", (event) => {
      if (event.propertyName !== "min-height") return;
      const start = performance.now();
      const sample = () => {
        window.activityShrinkHeights.push(element.getBoundingClientRect().height);
        if (performance.now() - start < 250) requestAnimationFrame(sample);
      };
      requestAnimationFrame(sample);
    });
  });
  await deliver(end("tall", "bash", output), start("short", "read", { path: "schema.sql" }));
  await expect(group).toContainText("schema.sql");
  expect(await page.evaluate(() => window.activityReplacementHeight)).toBeGreaterThanOrEqual(tallHeight - 1);
  await expect.poll(async () => (await group.boundingBox()).height).toBeLessThan(tallHeight - 20);
  await expect(group).toHaveCSS("min-height", "0px");
  const shortHeight = (await group.boundingBox()).height;
  await expect.poll(() => page.evaluate(({ tallHeight, shortHeight }) => window.activityShrinkHeights.some((height) => height > shortHeight + 1 && height < tallHeight - 1), { tallHeight, shortHeight })).toBe(true);
  await deliver(end("short", "read"));
  await expect(group.locator(".active-activity-count")).toHaveText("Done");
  await page.getByRole("switch", { name: "Show agent activity" }).tap();
  await page.waitForTimeout(1600);
  await expect(group).toHaveCount(0);
  await expect(page.locator('[data-tool-call-id="short"]')).toBeVisible();
  await page.getByRole("switch", { name: "Show agent activity" }).tap();
  await expect(group).toHaveCount(0);
});

test("a completion timer cannot move a card during its first Expand tap", async ({ page }) => {
  const deliver = await liveEvents(page);
  const group = activeGroup(page);
  const output = Array.from({ length: 40 }, (_, index) => `Completed line ${index + 1}`).join("\n");
  await deliver({ type: "agent_start" }, start("completed-touch", "bash", { command: "inspect" }));
  await expect(group).toContainText("1 running");
  await deliver(end("completed-touch", "bash", output));
  await expect(group.locator(".active-activity-count")).toHaveText("Done");
  const expand = group.getByRole("button", { name: "Expand", exact: true });
  await expand.scrollIntoViewIfNeeded();
  const box = await expand.boundingBox();
  const touch = await page.context().newCDPSession(page);
  await touch.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x: box.x + box.width / 2, y: box.y + box.height / 2 }] });
  await page.waitForTimeout(1500);
  await expect(expand).toBeVisible();
  await touch.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
  await touch.detach();
  await expect(group).toContainText("Pi is working…");
  await page.getByRole("switch", { name: "Show agent activity" }).tap();
  await expect(page.locator('[data-tool-call-id="completed-touch"] [data-tool-output-collapse]')).toHaveAttribute("data-expanded", "true");
});

test("discarded thinking is not resurrected by its completion timer", async ({ page }) => {
  const deliver = await liveEvents(page);
  const thinking = { type: "thinking", thinking: "Tentative reasoning that was removed." };
  await deliver({ type: "agent_start" }, { type: "message_update", message: assistant([thinking]), assistantMessageEvent: { type: "thinking_delta", contentIndex: 0 } });
  const group = activeGroup(page);
  await expect(group).toContainText(thinking.thinking);
  await deliver({ type: "message_update", message: assistant([thinking]), assistantMessageEvent: { type: "thinking_end", contentIndex: 0 } });
  await expect(group.locator(".active-activity-count")).toHaveText("Done");
  await deliver({ type: "message_end", message: assistant([{ type: "text", text: "Final answer without the tentative reasoning." }]) });
  await expect(group).toContainText("Pi is working…");
  await expect(page.locator(".message--thinking").filter({ hasText: thinking.thinking })).toHaveCount(0);
  await deliver({ type: "agent_settled" });
  await expect(group).toHaveCount(0);
});

test("joins the latest activity summary into the active group", async ({ page }) => {
  const deliver = await liveEvents(page);
  const group = activeGroup(page);
  const tools = [call("joined-read", "read", { path: "schema.sql" }), call("joined-bash", "bash", { command: "npm test" })];
  await deliver({ type: "agent_start" }, { type: "message_start", message: assistant(tools) }, { type: "message_end", message: assistant(tools) }, start("joined-read", "read", tools[0].arguments), end("joined-read", "read"), start("joined-bash", "bash", tools[1].arguments));
  const summary = group.locator(".focus-activity-summary");
  await expect(summary).toContainText("1 tool update");
  await expect(page.locator(".focus-activity-summary")).toHaveCount(1);
  await summary.getByRole("button").tap();
  const details = summary.locator(".focus-activity-details");
  await expect(details).toContainText("schema.sql");
  // Measure both in one frame because the summary may be rebuilt after the tap.
  expect(await group.evaluate((element) => element.querySelector(".focus-activity-details").getBoundingClientRect().bottom <= element.querySelector('[data-tool-call-id="joined-bash"]').getBoundingClientRect().top)).toBe(true);
  await group.screenshot({ path: test.info().outputPath("joined-active-now.png") });
  await deliver(end("joined-bash", "bash"));
  await expect(group).toContainText("Pi is working…");
  await expect(summary).toContainText("2 tool updates");
  await deliver({ type: "agent_end" });
  await expect(group).toHaveCount(0);
  await expect(page.locator(".focus-activity-summary")).toContainText("2 tool updates");
  await expect(page.locator(".focus-activity-details")).toContainText("npm test");
});

test("keeps keyboard focus on the joined summary toggle when it moves", async ({ page }) => {
  const deliver = await liveEvents(page);
  const group = activeGroup(page);
  const tools = [call("focus-read", "read", { path: "schema.sql" }), call("focus-test", "bash", { command: "npm test" }), call("focus-lint", "bash", { command: "npm run lint" })];
  await deliver({ type: "agent_start" }, { type: "message_start", message: assistant(tools) }, { type: "message_end", message: assistant(tools) }, start("focus-read", "read", tools[0].arguments), end("focus-read", "read"), ...tools.slice(1).map((tool) => start(tool.id, tool.name, tool.arguments)));
  const joinedToggle = group.locator("[data-focus-activity-toggle]");
  await expect(joinedToggle).toContainText("1 tool update");
  await joinedToggle.focus();
  await deliver(end("focus-lint", "bash"));
  await expect(joinedToggle).toContainText("2 tool updates");
  await expect(joinedToggle).toBeFocused();
  await deliver({ type: "agent_end" });
  await expect(group).toHaveCount(0);
  await expect(page.locator("[data-focus-activity-toggle]")).toBeFocused();
});

test("keeps a summary separate when text follows it", async ({ page }) => {
  const deliver = await liveEvents(page);
  const group = activeGroup(page);
  const read = call("separate-read", "read", { path: "schema.sql" });
  const bash = call("separate-bash", "bash", { command: "npm test" });
  const text = assistant([{ type: "text", text: "Schema looks fine, running tests." }]);
  await deliver({ type: "agent_start" }, { type: "message_start", message: assistant([read]) }, { type: "message_end", message: assistant([read]) }, start(read.id, read.name, read.arguments), end(read.id, read.name), { type: "message_start", message: text }, { type: "message_end", message: text }, { type: "message_start", message: assistant([bash]) }, { type: "message_end", message: assistant([bash]) }, start(bash.id, bash.name, bash.arguments));
  await expect(group).toContainText("1 running");
  await expect(page.locator(".focus-activity-summary")).toContainText("1 tool update");
  await expect(group.locator(".focus-activity-summary")).toHaveCount(0);
});

test("text-only runs do not create an activity group", async ({ page }) => {
  const deliver = await liveEvents(page);
  await deliver({ type: "agent_start" }, { type: "message_update", message: assistant([{ type: "text", text: "Answer without tools." }]), assistantMessageEvent: { type: "text_delta", contentIndex: 0 } });
  await expect(page.locator(".message").filter({ hasText: "Answer without tools." })).toBeVisible();
  await expect(activeGroup(page)).toHaveCount(0);
});

test("groups parallel cards, summarizes each completion and restores original order", async ({ page }) => {
  const deliver = await liveEvents(page);
  const tools = [call("active-bash", "bash", { command: "npm test" }), call("active-read", "read", { path: "app.js" })];
  await deliver({ type: "agent_start" }, { type: "message_start", message: assistant(tools) }, { type: "message_end", message: assistant(tools) }, start("active-bash", "bash", tools[0].arguments), start("active-read", "read", tools[1].arguments));
  const group = activeGroup(page);
  await expect(group).toBeVisible();
  await expect(group.locator(".message")).toHaveCount(2);
  await expect(group).toContainText("2 running");
  await group.screenshot({ path: test.info().outputPath("parallel-active-now.png") });
  const bash = page.locator('[data-tool-call-id="active-bash"]');
  const read = page.locator('[data-tool-call-id="active-read"]');
  await bash.evaluate((card) => { window.originalActivityCard = card; });
  // A normal assistant message must not be swallowed by the group.
  await deliver({ type: "message_update", message: assistant([...tools, { type: "text", text: "Both checks are underway." }]), assistantMessageEvent: { type: "text_delta", contentIndex: 2 } });
  await expect(page.locator(".message").filter({ hasText: "Both checks are underway." })).toBeVisible();
  await expect(group).not.toContainText("Both checks are underway.");
  const toggle = page.getByRole("switch", { name: "Show agent activity" });
  await toggle.tap();
  await expect(group).toHaveCount(0);
  expect(await page.locator('#live-output > .message').evaluateAll((cards) => cards.map((card) => card.dataset.toolCallId || "text"))).toEqual(["active-bash", "active-read", "text"]);
  expect(await bash.evaluate((card) => card === window.originalActivityCard)).toBe(true);
  await toggle.tap();
  await expect(group.locator(".message")).toHaveCount(2);
  await deliver(end("active-read", "read"));
  await expect(read).toBeHidden();
  await expect(group.locator(".message")).toHaveCount(1);
  await expect(group).toContainText("1 running");
  await expect(page.locator(".focus-activity-summary").last()).toContainText("1 tool update");
  await deliver(end("active-bash", "bash", "Test failed", true));
  await expect(group.locator(".active-activity-count")).toHaveText("Failed");
  await expect(group).toContainText("Pi is working…");
  await expect(bash).toBeHidden();
  await expect(page.locator(".focus-activity-summary").last()).toContainText("2 tool updates");
  await expect(page.locator(".focus-activity-summary").last()).toContainText("1 error");
  // Let the pending height refresh finish so only the correction below can rebuild the summary.
  await expect(group).toHaveCSS("min-height", "0px");
  const toggleSummary = page.locator("[data-focus-activity-toggle]").last();
  await toggleSummary.scrollIntoViewIfNeeded();
  const box = await toggleSummary.boundingBox();
  const touch = await page.context().newCDPSession(page);
  await touch.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x: box.x + box.width / 2, y: box.y + box.height / 2 }] });
  // A correction to completed output must not replace the summary during a tap.
  await deliver(end("active-bash", "bash", "Corrected output"));
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  await touch.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
  await touch.detach();
  await expect(page.locator(".focus-activity-details").last()).toBeVisible();
});

for (const transport of ["message", "gatewayPartialMessage"]) {
  test(`${transport} shows only current thinking and preparation`, async ({ page }) => {
    const deliver = await liveEvents(page);
    const thinking = { type: "thinking", thinking: "Checking the visibility rules." };
    const update = (type, content, index = 0) => ({ type: "message_update", assistantMessageEvent: { type, contentIndex: index }, [transport]: assistant(content) });
    await deliver({ type: "agent_start" }, update("thinking_delta", [thinking]));
    const group = activeGroup(page);
    await expect(group.locator(".message--thinking")).toContainText(thinking.thinking);
    if (transport === "message") await group.screenshot({ path: test.info().outputPath("thinking-active-now.png") });
    await page.keyboard.press("Control+f");
    await page.getByRole("searchbox", { name: "Find in conversation" }).fill("visibility rules");
    await expect(page.locator("[data-current-session-find-count]")).toHaveText("1 / 1");
    await page.getByRole("checkbox", { name: "Conversation only" }).check();
    await expect(page.locator("[data-current-session-find-count]")).toHaveText("0 / 0");
    await page.getByRole("button", { name: "Close find" }).click();
    await deliver(update("thinking_end", [thinking]));
    await expect(group.locator(".active-activity-count")).toHaveText("Done");
    await deliver(update("toolcall_delta", [thinking], 1));
    await expect(group.locator(".message--tool-preparation")).toBeVisible();
    await expect(group.locator(".message--thinking")).toHaveCount(0);
    const tool = call("prepared-call", "write", { path: "example.js", content: "example" });
    await deliver(update("toolcall_end", [thinking, tool], 1));
    await expect(group.locator('[data-tool-call-id="prepared-call"]')).toBeVisible();
    await expect(page.locator(".message--tool-preparation")).toHaveCount(0);
    await deliver({ type: "agent_settled" });
    await expect(group).toHaveCount(0);
  });
}

test("focused activity does not scroll past the beginning of an oversized answer", async ({ page }) => {
  const deliver = await liveEvents(page);
  await deliver({ type: "agent_start" }, start("before-answer", "bash", { command: "inspect" }));
  await expect(activeGroup(page)).toBeVisible();
  await deliver(end("before-answer", "bash"));
  await expect(activeGroup(page).locator(".active-activity-count")).toHaveText("Done");
  const short = "Here are the findings.";
  await deliver({ type: "message_start", message: assistant([]) }, { type: "message_update", message: assistant([{ type: "text", text: short }]), assistantMessageEvent: { type: "text_delta", contentIndex: 0 } });
  const card = page.locator('.message--assistant').filter({ hasText: short });
  await expect(card).toBeVisible();
  // Let the short answer's initial auto-follow settle before it grows.
  await page.waitForTimeout(350);
  const text = short + "\n\n" + Array.from({ length: 80 }, (_, index) => `Finding ${index + 1}: keep the start of this answer visible.`).join("\n\n");
  await deliver({ type: "message_update", message: assistant([{ type: "text", text }]), assistantMessageEvent: { type: "text_delta", contentIndex: 0 } });
  await expect(card).toContainText("Finding 80:");
  await expect.poll(() => card.evaluate((element) => Math.abs(element.getBoundingClientRect().top - document.querySelector('#conversation-scroll').getBoundingClientRect().top))).toBeLessThan(40);
});

test("active output and subagent prompts open on the first tap during updates", async ({ page }) => {
  const deliver = await liveEvents(page);
  const tool = call("touch-bash", "bash", { command: "npm test" });
  const output = Array.from({ length: 40 }, (_, index) => `Output line ${index + 1}`).join("\n");
  await deliver({ type: "agent_start" }, { type: "message_start", message: assistant([tool]) }, start(tool.id, tool.name, tool.arguments), { type: "tool_execution_update", toolCallId: tool.id, toolName: tool.name, partialResult: { content: [{ type: "text", text: output }] } }, start("touch-subagent", "subagent", { task: "Review activity visibility and touch behavior" }));
  const group = activeGroup(page);
  const prompt = group.locator(".subagent-prompt");
  await prompt.locator("summary").tap();
  await expect(prompt).toHaveAttribute("open", "");
  const expand = group.getByRole("button", { name: "Expand", exact: true });
  await expand.scrollIntoViewIfNeeded();
  const box = await expand.boundingBox();
  const touch = await page.context().newCDPSession(page);
  await touch.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x: box.x + box.width / 2, y: box.y + box.height / 2 }] });
  await deliver(end("touch-subagent", "subagent"));
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  await touch.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
  await touch.detach();
  await expect(group.locator('[data-tool-output-collapse][data-expanded="true"]')).toHaveCount(1);
  await expect(group.locator(".message")).toHaveCount(1);
  await expect(group).toContainText("Output line 1");
  await group.screenshot({ path: test.info().outputPath("active-now.png") });
});

test("reload restores only the still-running parallel subagent", async ({ page }) => {
  const mobile = page.viewportSize().width < 768;
  const scenario = mobile ? mobileSubagents : subagents;
  const title = mobile ? sessions.activeSubagentsMobile : sessions.activeSubagents;
  await page.goto(`/?session_search=${encodeURIComponent(title)}`);
  await page.goto(await page.getByRole("link", { name: new RegExp(title) }).getAttribute("href"));
  await sendPrompt(page, mobile ? prompts.parallelSubagentsMobile : prompts.parallelSubagents);
  await expect(page.locator(`article[data-tool-call-id="${scenario.firstCallId}"]`)).toContainText(scenario.firstResult);
  await expect(page.locator(`article[data-tool-call-id="${scenario.secondCallId}"]`)).toContainText(scenario.secondProgress);
  await page.reload();
  await page.getByRole("switch", { name: "Show agent activity" }).tap();
  const group = activeGroup(page);
  await expect(group.locator(".message")).toHaveCount(1);
  await expect(group).toContainText(scenario.secondProgress);
  await expect(page.locator(`article[data-tool-call-id="${scenario.firstCallId}"]`)).toBeHidden();
  await page.getByRole("button", { name: "Abort running Pi" }).tap();
  await expectRunFinished(page);
  await expect(group).toHaveCount(0);
  const session = new URL(page.url()).searchParams.get("session");
  const deleted = await page.request.post("/sessions/delete", { form: { session } });
  expect(deleted.ok()).toBe(true);
});
