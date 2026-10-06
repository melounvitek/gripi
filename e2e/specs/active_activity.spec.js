import { expect, test } from "@playwright/test";
import { mobileSubagents, prompts, sessions, subagents } from "../support/contract.mjs";
import { activityView, expectRunFinished, sendPrompt } from "../support/ui.mjs";

test.use({ hasTouch: true });

async function liveEvents(page) {
  const events = [];
  await page.route(/\/events(?:\?|$)/, async (route) => {
    const after = Number(new URL(route.request().url()).searchParams.get("after") || 0);
    await route.fulfill({ json: { events: events.slice(after), last_seq: events.length, missed: false } });
  });
  await page.goto(`/?session_search=${encodeURIComponent(sessions.marker)}`);
  await page.goto(await page.getByRole("link", { name: new RegExp(sessions.marker) }).getAttribute("href"));
  await activityView(page, "Brief").tap();
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

test("tool images survive activity retirement, including standalone custom-tool results", async ({ page }) => {
  const deliver = await liveEvents(page);
  const content = [
    { type: "text", text: "Screenshot captured" },
    { type: "image", mimeType: "image/png", data: (await page.screenshot()).toString("base64") }
  ];
  await deliver({ type: "agent_start" }, start("image-tool", "screenshot", {}));
  await expect(activeGroup(page)).toContainText("1 running");
  await deliver(
    { ...end("image-tool", "screenshot"), result: { content } },
    { type: "message_end", message: { role: "toolResult", toolCallId: "image-tool", toolName: "screenshot", content } }
  );
  const card = page.locator('[data-tool-call-id="image-tool"]');
  await expect(card.locator(".message-images")).toBeVisible();
  await deliver({ type: "agent_end" });
  await expect(activeGroup(page)).toHaveCount(0);
  await expect(card.locator(".message-images")).toBeVisible();
  await expect(card.getByText("screenshot", { exact: true })).toBeVisible();
  await expect(card.getByText("Screenshot captured")).toBeHidden();

  await deliver({ type: "message_end", message: { role: "toolResult", toolCallId: "standalone-image", toolName: "screenshot", content } });
  const standalone = page.locator('[data-tool-call-id="standalone-image"]');
  await expect(standalone.locator(".message-images")).toBeVisible();
  await expect(standalone.getByText("screenshot", { exact: true })).toBeVisible();
  await expect(standalone.getByText("Screenshot captured")).toBeHidden();
  await page.screenshot({ path: test.info().outputPath("focused-tool-images.png") });
  // Find matches the two summary lines, not the hidden output below them.
  await page.keyboard.press("Control+f");
  await page.getByRole("searchbox", { name: "Find in conversation" }).fill("screenshot");
  await expect(page.locator("[data-current-session-find-count]")).toHaveText("1 / 2");
  await page.getByRole("button", { name: "Close find" }).click();
  await activityView(page, "Full").tap();
  await expect(card.getByText("Screenshot captured")).toBeVisible();
  await expect(standalone.getByText("Screenshot captured")).toBeVisible();
  await expect(page.locator(".message-images")).toHaveCount(2);
});

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
  // The finished step stays until Pi's next step starts.
  await page.waitForTimeout(1600);
  await expect(group.locator('[data-tool-call-id="gap-a"]')).toContainText("Database inspected");
  await expect(group.locator("[data-activity-active]")).toHaveCount(0);
  await page.screenshot({ path: test.info().outputPath("activity-gap.png") });
  await deliver({ type: "turn_start" }, start("gap-b", "read", { path: "schema.sql" }));
  await expect(group).toContainText("1 running");
  await expect(group.locator('[data-tool-call-id="gap-b"]')).toBeVisible();
  await expect(page.locator('[data-tool-call-id="gap-a"]')).toBeHidden();
  await deliver(end("gap-b", "read"));
  await expect(group.locator(".active-activity-count")).toHaveText("Done");
  await expect(group.locator('[data-tool-call-id="gap-b"]')).toBeVisible();
  expect(await group.evaluate((element) => element === window.activityShell)).toBe(true);
  await deliver(start("gap-c", "bash", { command: "verify schema" }));
  await expect(group).toContainText("1 running");
  await expect(page.locator('[data-tool-call-id="gap-b"]')).toBeHidden();
  await deliver({ type: "agent_end" });
  await expect(group).toHaveCount(0);
});

test("activity height holds through a run and starts fresh for the next run", async ({ page }) => {
  const deliver = await liveEvents(page);
  const group = activeGroup(page);
  const output = Array.from({ length: 10 }, () => "Long output line").join("\n");
  await deliver({ type: "agent_start" }, start("tall", "bash", { command: "inspect" }), { type: "tool_execution_update", toolCallId: "tall", toolName: "bash", partialResult: { content: [{ type: "text", text: output }] } });
  await expect(group).toContainText("Long output line");
  const tallHeight = (await group.boundingBox()).height;
  await group.evaluate((element) => {
    window.activityMinHeight = Infinity;
    const sample = () => {
      if (!element.isConnected) return;
      window.activityMinHeight = Math.min(window.activityMinHeight, element.getBoundingClientRect().height);
      requestAnimationFrame(sample);
    };
    requestAnimationFrame(sample);
  });
  await deliver(end("tall", "bash", output), start("short", "read", { path: "schema.sql" }));
  await expect(group).toContainText("schema.sql");
  await expect(group.locator(".focus-activity-summary")).toContainText("1 tool update");
  // The held space stays below the step instead of spreading out the rows.
  expect(await group.evaluate((element) => element.querySelector('[data-tool-call-id="short"]').getBoundingClientRect().top - element.querySelector(".focus-activity-header").getBoundingClientRect().bottom)).toBeLessThan(24);
  await deliver(end("short", "read"));
  await expect(group.locator(".active-activity-count")).toHaveText("Done");
  await page.waitForTimeout(600);
  expect(await page.evaluate(() => window.activityMinHeight)).toBeGreaterThanOrEqual(tallHeight - 1);
  await deliver({ type: "agent_end" });
  await expect(group).toHaveCount(0);
  await deliver({ type: "agent_start" }, start("next-run", "read", { path: "schema.sql" }));
  await expect(group).toContainText("1 running");
  expect((await group.boundingBox()).height).toBeLessThan(tallHeight - 20);
});

test("a step taller than the view does not hold later steps out of view", async ({ page }) => {
  const deliver = await liveEvents(page);
  const group = activeGroup(page);
  const thinking = { type: "thinking", thinking: Array.from({ length: 30 }, (_, index) => `Long reasoning paragraph ${index + 1}.`).join("\n\n") };
  const update = (type) => ({ type: "message_update", message: assistant([thinking]), assistantMessageEvent: { type, contentIndex: 0 } });
  await deliver({ type: "agent_start" }, update("thinking_delta"));
  await expect(group).toContainText("Long reasoning paragraph 30.");
  await deliver(update("thinking_end"), start("after-thinking", "read", { path: "schema.sql" }));
  await expect(group.locator('[data-tool-call-id="after-thinking"]')).toBeVisible();
  await expect.poll(() => page.locator("#conversation-scroll").evaluate((element) => element.scrollHeight - element.scrollTop - element.clientHeight)).toBeLessThan(2);
  await expect(group.locator('[data-tool-call-id="after-thinking"]')).toBeInViewport();
  // The group header stays below the top edge with the conversation's usual top padding, allowing for sub-pixel rounding.
  await expect.poll(() => page.locator("#conversation-scroll").evaluate((element) => {
    const header = element.querySelector(".active-activity .focus-activity-header");
    return header.getBoundingClientRect().top - element.getBoundingClientRect().top - parseFloat(getComputedStyle(element).paddingTop);
  })).toBeGreaterThan(-1);
});

test("steps that start and finish between updates still show as the finished step", async ({ page }) => {
  const deliver = await liveEvents(page);
  const group = activeGroup(page);
  const read = call("quick-read", "read", { path: "schema.sql" });
  // Quick tools start and finish within one batch, so the page never sees them running.
  await deliver({ type: "agent_start" }, { type: "message_end", message: assistant([read]) }, start(read.id, read.name, read.arguments), end(read.id, read.name));
  await expect(group.locator('[data-tool-call-id="quick-read"]')).toBeVisible();
  await expect(group.locator(".active-activity-count")).toHaveText("Done");

  const update = (type, content) => ({ type: "message_update", message: assistant(content), assistantMessageEvent: { type, contentIndex: 0 } });
  await deliver({ type: "turn_end" }, { type: "turn_start" }, update("toolcall_delta", []));
  await expect(group.locator(".message--tool-preparation")).toBeVisible();
  const bash = call("quick-bash", "bash", { command: "npm test" });
  await deliver(update("toolcall_end", [bash]), { type: "message_end", message: assistant([bash]) }, start(bash.id, bash.name, bash.arguments), end(bash.id, bash.name, "Tests passed"));
  await expect(group.locator('[data-tool-call-id="quick-bash"]')).toBeVisible();
  await expect(page.locator('[data-tool-call-id="quick-read"]')).toBeHidden();
  await expect(group.locator(".focus-activity-summary")).toContainText("1 tool update");
  await deliver({ type: "agent_end" });
  await expect(group).toHaveCount(0);
});

test("the next step cannot move a finished card during its first Expand tap", async ({ page }) => {
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
  await deliver(start("after-touch", "read", { path: "schema.sql" }));
  await expect(page.locator('[data-tool-call-id="after-touch"]')).toHaveCount(1);
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  await expect(expand).toBeVisible();
  await touch.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
  await touch.detach();
  await expect(group.locator('[data-tool-call-id="after-touch"]')).toBeVisible();
  await expect(page.locator('[data-tool-call-id="completed-touch"]')).toBeHidden();
  await activityView(page, "Full").tap();
  await expect(page.locator('[data-tool-call-id="completed-touch"] [data-tool-output-collapse]')).toHaveAttribute("data-expanded", "true");
});

test("discarded thinking is not kept as the finished step", async ({ page }) => {
  const deliver = await liveEvents(page);
  const thinking = { type: "thinking", thinking: "Tentative reasoning that was removed." };
  await deliver({ type: "agent_start" }, { type: "message_update", message: assistant([thinking]), assistantMessageEvent: { type: "thinking_delta", contentIndex: 0 } });
  const group = activeGroup(page);
  await expect(group).toContainText(thinking.thinking);
  await deliver({ type: "message_update", message: assistant([thinking]), assistantMessageEvent: { type: "thinking_end", contentIndex: 0 } });
  await expect(group.locator(".active-activity-count")).toHaveText("Done");
  await deliver({ type: "message_end", message: assistant([{ type: "text", text: "Final answer without the tentative reasoning." }]) });
  await expect(group).toHaveCount(0);
  await expect(page.locator(".message--thinking").filter({ hasText: thinking.thinking })).toHaveCount(0);
});

test("a reply takes the place of the finished step", async ({ page }) => {
  const deliver = await liveEvents(page);
  const group = activeGroup(page);
  const earlier = Array.from({ length: 40 }, (_, index) => `Earlier paragraph ${index + 1}.`).join("\n\n");
  await deliver({ type: "agent_start" }, { type: "message_end", message: assistant([{ type: "text", text: earlier }]) }, { type: "agent_end" });
  await expect(page.locator(".message--assistant").filter({ hasText: "Earlier paragraph 40." })).toBeVisible();
  const output = Array.from({ length: 10 }, (_, index) => `Inspection line ${index + 1}`).join("\n");
  await deliver({ type: "agent_start" }, start("before-reply", "bash", { command: "inspect" }));
  await expect(group).toContainText("1 running");
  await deliver(end("before-reply", "bash", output));
  await expect(group.locator(".active-activity-count")).toHaveText("Done");
  const text = "Here is what the inspection found.";
  await deliver({ type: "message_start", message: assistant([]) }, { type: "message_update", message: assistant([{ type: "text", text }]), assistantMessageEvent: { type: "text_delta", contentIndex: 0 } });
  const reply = page.locator(".message--assistant").filter({ hasText: text });
  await expect(reply).toBeVisible();
  await expect(group).toHaveCount(0);
  const summary = page.locator(".focus-activity-summary").last();
  await expect(summary).toContainText("1 tool update");
  // Measure both in one frame because the page may still be following the reply.
  expect(await reply.evaluate((element) => [...document.querySelectorAll(".focus-activity-summary")].at(-1).getBoundingClientRect().bottom <= element.getBoundingClientRect().top)).toBe(true);
  await expect.poll(() => page.locator("#conversation-scroll").evaluate((element) => element.scrollHeight - element.scrollTop - element.clientHeight)).toBeLessThan(2);
  // Measure from the conversation's bottom edge, which moves when the composer's working line goes.
  const bottomGap = () => reply.evaluate((element) => document.querySelector("#conversation-scroll").getBoundingClientRect().bottom - element.getBoundingClientRect().bottom);
  const gap = await bottomGap();
  await deliver({ type: "message_end", message: assistant([{ type: "text", text }]) }, { type: "agent_end" }, { type: "agent_settled" });
  // The run's events arrive together, so the finished reply means they were all handled.
  await expect(reply).not.toHaveClass(/message--streaming/);
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  expect(Math.abs(await bottomGap() - gap)).toBeLessThan(2);
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
  await expect(group.locator(".active-activity-count")).toHaveText("Done");
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
  await activityView(page, "Full").tap();
  await expect(group).toHaveCount(0);
  expect(await page.locator('#live-output > .message').evaluateAll((cards) => cards.map((card) => card.dataset.toolCallId || "text"))).toEqual(["active-bash", "active-read", "text"]);
  expect(await bash.evaluate((card) => card === window.originalActivityCard)).toBe(true);
  await activityView(page, "Brief").tap();
  await expect(group.locator(".message")).toHaveCount(2);
  const parallelHeight = (await group.boundingBox()).height;
  await deliver(end("active-read", "read"));
  await expect(read).toBeHidden();
  await expect(group.locator(".message")).toHaveCount(1);
  // Only single-step heights are held, so the box shrinks once parallel tools finish.
  await expect.poll(async () => (await group.boundingBox()).height).toBeLessThan(parallelHeight - 20);
  await expect(group).toContainText("1 running");
  await expect(page.locator(".focus-activity-summary").last()).toContainText("1 tool update");
  await deliver(end("active-bash", "bash", "Test failed", true));
  await expect(group.locator(".active-activity-count")).toHaveText("Failed");
  await expect(group.locator('[data-tool-call-id="active-bash"]')).toBeVisible();
  await deliver(start("active-next", "read", { path: "README.md" }));
  await expect(bash).toBeHidden();
  await expect(page.locator(".focus-activity-summary").last()).toContainText("2 tool updates");
  await expect(page.locator(".focus-activity-summary").last()).toContainText("1 error");
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

for (const view of ["Full", "Brief"]) {
  const answer = (text) => ({ type: "message_update", message: assistant([{ type: "text", text }]), assistantMessageEvent: { type: "text_delta", contentIndex: 0 } });
  const finding = (index) => `Finding ${index}: keep the start of this answer visible while it streams.\n\n`;
  const topOffset = (card) => card.evaluate((element) => Math.abs(element.getBoundingClientRect().top - document.querySelector("#conversation-scroll").getBoundingClientRect().top));

  test(`${view} keeps the start of a long answer in view while it streams`, async ({ page }) => {
    test.setTimeout(40_000);
    const deliver = await liveEvents(page);
    await activityView(page, view).tap();
    await deliver({ type: "agent_start" }, { type: "message_start", message: assistant([]) });
    let text = "";
    // Stream at Pi's pace, so the page scrolls while earlier scrolls are still settling.
    for (let index = 1; index <= 30; index += 1) {
      text += finding(index);
      await deliver(answer(text));
      await page.waitForTimeout(120);
    }
    const card = page.locator(".message--assistant").filter({ hasText: "Finding 1:" });
    await expect(card).toContainText("Finding 30:");
    await expect.poll(() => topOffset(card)).toBeLessThan(40);
  });

  test(`${view} keeps the start of a long answer in view when the page shrinks below it`, async ({ page }) => {
    const deliver = await liveEvents(page);
    await activityView(page, view).tap();
    const scroller = page.locator("#conversation-scroll");
    await deliver({ type: "agent_start" }, { type: "message_start", message: assistant([]) }, answer(finding(1)));
    const card = page.locator(".message--assistant").filter({ hasText: "Finding 1:" });
    await expect(card).toBeVisible();
    // Stands in for a step below the answer that collapses without the user scrolling.
    await scroller.evaluate((element) => element.insertAdjacentHTML("beforeend", '<div data-shrinking-step style="height: 400px"></div>'));
    await deliver(answer(finding(1) + finding(2)));
    await expect.poll(() => scroller.evaluate((element) => element.scrollHeight - element.scrollTop - element.clientHeight)).toBeLessThan(2);
    await page.waitForTimeout(300);
    await page.locator("[data-shrinking-step]").evaluate((element) => element.remove());
    await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    await deliver(answer(Array.from({ length: 60 }, (_, index) => finding(index + 1)).join("")));
    await expect(card).toContainText("Finding 60:");
    await expect.poll(() => topOffset(card)).toBeLessThan(40);
  });
}

async function streamingReplyAtBottom(page) {
  const deliver = await liveEvents(page);
  const scroller = page.locator("#conversation-scroll");
  const earlier = Array.from({ length: 40 }, (_, index) => `Earlier paragraph ${index + 1}.`).join("\n\n");
  await deliver({ type: "agent_start" }, { type: "message_end", message: assistant([{ type: "text", text: earlier }]) }, { type: "agent_end" });
  await expect(page.locator(".message--assistant").filter({ hasText: "Earlier paragraph 40." })).toBeVisible();
  let text = "Streaming reply.";
  const update = () => deliver({ type: "message_update", message: assistant([{ type: "text", text }]), assistantMessageEvent: { type: "text_delta", contentIndex: 0 } });
  await deliver({ type: "agent_start" }, { type: "message_start", message: assistant([]) });
  await update();
  const fromBottom = () => scroller.evaluate((element) => element.scrollHeight - element.scrollTop - element.clientHeight);
  await expect.poll(fromBottom).toBeLessThan(2);
  return {
    box: await scroller.boundingBox(),
    fromBottom,
    async expectNotFollowing() {
      // Let a fling finish first, or it could hide a wrong follow scroll.
      await expect.poll(async () => {
        const before = await fromBottom();
        await page.waitForTimeout(150);
        return before === await fromBottom();
      }).toBe(true);
      text += " More.";
      await update();
      await expect(page.locator(".message--assistant").filter({ hasText: text })).toHaveCount(1);
      // Leave time for the follow scroll that must not happen.
      await page.waitForTimeout(500);
      expect(await fromBottom()).toBeGreaterThan(120);
    }
  };
}

test("a touch drag that pauses still stops following a streaming reply", async ({ page }) => {
  const { box, fromBottom, expectNotFollowing } = await streamingReplyAtBottom(page);
  const touch = await page.context().newCDPSession(page);
  const x = box.x + box.width / 2;
  const move = async (from, to) => {
    for (let step = 1; step <= 5; step += 1) await touch.send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: [{ x, y: from + (to - from) * step / 5 }] });
  };
  await touch.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x, y: box.y + 100 }] });
  await move(box.y + 100, box.y + 150);
  await page.waitForTimeout(600);
  await move(box.y + 150, box.y + 400);
  await touch.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
  await touch.detach();
  await expect.poll(fromBottom).toBeGreaterThan(120);
  await expectNotFollowing();
});

test("a text selection that scrolls the view stops following a streaming reply", async ({ page, isMobile }) => {
  test.skip(isMobile, "Selecting with a mouse is a desktop interaction");
  const { box, fromBottom, expectNotFollowing } = await streamingReplyAtBottom(page);
  const x = box.x + 60;
  await page.mouse.move(x, box.y + box.height - 100);
  await page.mouse.down();
  await page.mouse.move(x, box.y + box.height - 200, { steps: 5 });
  // Pause so the scrolling starts well after the press.
  await page.waitForTimeout(400);
  // Holding the selection above the conversation scrolls it with no further input, like a scrollbar drag.
  await page.mouse.move(x, box.y - 20, { steps: 5 });
  await page.waitForTimeout(800);
  await page.mouse.up();
  await expect.poll(fromBottom).toBeGreaterThan(120);
  // Clear the selection, which pauses following on its own.
  await page.mouse.click(x, box.y + box.height / 2);
  await expectNotFollowing();
});

test("the bottom jump button waits for the reader to scroll into a long answer", async ({ page, isMobile }) => {
  test.skip(isMobile, "Narrow screens only show the jump buttons while the reader scrolls");
  const deliver = await liveEvents(page);
  const scroller = page.locator("#conversation-scroll");
  const jump = page.locator(".jump-to-latest");
  const fromBottom = () => scroller.evaluate((element) => element.scrollHeight - element.scrollTop - element.clientHeight);
  const paragraphs = (name) => Array.from({ length: 60 }, (_, index) => `${name} paragraph ${index + 1}.`).join("\n\n");
  await deliver({ type: "agent_start" }, { type: "message_end", message: assistant([{ type: "text", text: paragraphs("Earlier") }]) }, { type: "agent_end" });
  await expect(page.locator(".message--assistant").filter({ hasText: "Earlier paragraph 60." })).toBeVisible();
  await expect.poll(fromBottom).toBeLessThan(2);
  // The reader's last scroll is downwards, back to the latest output.
  const box = await scroller.boundingBox();
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.wheel(0, -600);
  await expect.poll(fromBottom).toBeGreaterThan(120);
  await page.mouse.wheel(0, 2000);
  await expect.poll(fromBottom).toBeLessThan(2);
  // Let that scroll finish, so the answer arrives with no scrolling under way.
  await page.waitForTimeout(300);
  await deliver({ type: "agent_start" }, { type: "message_start", message: assistant([]) }, { type: "message_update", message: assistant([{ type: "text", text: paragraphs("Next") }]), assistantMessageEvent: { type: "text_delta", contentIndex: 0 } });
  const card = page.locator(".message--assistant").filter({ hasText: "Next paragraph 60." });
  // The page stops at the start of the answer, with its end below the view.
  await expect.poll(() => card.evaluate((element) => Math.abs(element.getBoundingClientRect().top - document.querySelector("#conversation-scroll").getBoundingClientRect().top))).toBeLessThan(40);
  await expect(jump).toBeHidden();
  await page.mouse.wheel(0, 300);
  await expect(jump).toBeVisible();
});

test("a streaming reply keeps rendering while Markdown responses are slow", async ({ page }) => {
  const deliver = await liveEvents(page);
  await page.route("**/markdown", async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 400));
    await route.continue();
  });
  await deliver({ type: "agent_start" }, { type: "message_start", message: assistant([]) });
  const reply = page.locator(".message--assistant").filter({ hasText: "Point 1" });
  const renderedCounts = new Set();
  let text = "";
  // Updates arrive faster than the slow renders return.
  for (let index = 1; index <= 25; index += 1) {
    text += `**Point ${index}** keeps streaming.\n\n`;
    await deliver({ type: "message_update", message: assistant([{ type: "text", text }]), assistantMessageEvent: { type: "text_delta", contentIndex: 0 } });
    await page.waitForTimeout(100);
    const rendered = await reply.locator("strong").count();
    if (rendered) renderedCounts.add(rendered);
  }
  expect(renderedCounts.size).toBeGreaterThan(1);
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
  await activityView(page, "Brief").tap();
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
