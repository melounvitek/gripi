import { expect, test } from "@playwright/test";
import { sessions } from "../support/contract.mjs";

async function liveEvents(page) {
  const events = [];
  await page.route(/\/events(?:\?|$)/, async (route) => {
    const after = Number(new URL(route.request().url()).searchParams.get("after") || 0);
    await route.fulfill({ json: { events: events.slice(after), last_seq: events.length, missed: false } });
  });
  await page.goto(`/?session_search=${encodeURIComponent(sessions.marker)}`);
  await page.goto(await page.getByRole("link", { name: new RegExp(sessions.marker) }).getAttribute("href"));
  return async (...updates) => {
    events.push(...updates);
    await page.evaluate(() => window.dispatchEvent(new Event("pageshow")));
  };
}

const assistant = (content) => ({ role: "assistant", content });
const call = (id, name, args) => ({ type: "toolCall", id, name, arguments: args });
const start = (id, name, args) => ({ type: "tool_execution_start", toolCallId: id, toolName: name, args });
const end = (id, name, isError = false) => ({ type: "tool_execution_end", toolCallId: id, toolName: name, result: { content: [{ type: "text", text: "Finished" }] }, isError });
const result = (id, name, isError = false) => ({ type: "message_end", message: { role: "toolResult", toolCallId: id, toolName: name, content: [{ type: "text", text: "Finished" }], isError } });

test("live tool calls stay pending until their result arrives", async ({ page }) => {
  const deliver = await liveEvents(page);
  const tools = [call("state-ok", "bash", { command: "make check" }), call("state-fail", "bash", { command: "make lint" })];
  await deliver({ type: "agent_start" }, { type: "message_start", message: assistant(tools) }, { type: "message_end", message: assistant(tools) });
  const ok = page.locator('[data-tool-call-id="state-ok"]');
  const failed = page.locator('[data-tool-call-id="state-fail"]');
  await expect(ok).toHaveClass(/message--tool-pending/);
  await expect(failed).toHaveClass(/message--tool-pending/);

  await deliver(start("state-ok", "bash", tools[0].arguments), end("state-ok", "bash"), result("state-ok", "bash"));
  await expect(ok).not.toHaveClass(/message--tool-pending/);
  await expect(ok).not.toHaveClass(/message--tool-error/);
  await deliver(start("state-fail", "bash", tools[1].arguments), end("state-fail", "bash", true), result("state-fail", "bash", true));
  await expect(failed).not.toHaveClass(/message--tool-pending/);
  await expect(failed).toHaveClass(/message--tool-error/);

  const pendingCustom = page.locator('[data-tool-call-id="state-shot"].message--tool-pending');
  await deliver(start("state-shot", "screenshot", {}));
  await expect(pendingCustom).not.toHaveCount(0);
  await deliver(end("state-shot", "screenshot"), result("state-shot", "screenshot"), { type: "agent_end" });
  await expect(page.locator('[data-tool-call-id="state-shot"]')).not.toHaveCount(0);
  await expect(pendingCustom).toHaveCount(0);
});
