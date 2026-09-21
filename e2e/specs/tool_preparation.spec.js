import { expect, test } from "@playwright/test";

async function eventDelivery(page) {
  const pending = [];
  let sequence = 0;
  await page.route(/\/events(?:\?|$)/, async (route) => {
    const events = pending.splice(0);
    sequence += events.length;
    await route.fulfill({ json: { events, last_seq: sequence, missed: false } });
  });
  await page.goto("/");
  await expect(page.locator("#live-output")).toBeAttached();
  return async (...events) => {
    pending.push(...events);
    await page.evaluate(() => window.dispatchEvent(new Event("pageshow")));
  };
}

for (const transport of ["delta-only", "cumulative"]) {
  test(`${transport} tool preparation replaces the text cursor and preserves rendered text`, async ({ page }) => {
    const deliver = await eventDelivery(page);
    const text = "I will now write the update script.";
    const message = { role: "assistant", timestamp: Date.now(), content: [{ type: "text", text }] };
    const update = (type, snapshot = message) => ({
      type: "message_update",
      assistantMessageEvent: { type, contentIndex: 1, delta: "script fragment" },
      [transport === "delta-only" ? "gatewayPartialMessage" : "message"]: snapshot,
    });
    const status = page.locator(".composer-state");
    const card = page.locator('.message[data-role="assistant"]').filter({ hasText: text });
    await deliver(
      { type: "agent_start" },
      { type: "message_start", message: { ...message, content: [] } },
      update("text_delta"),
    );
    await expect(card).toBeVisible();
    await expect(card).toHaveClass(/message--streaming/);

    // A coalesced delta can arrive without toolcall_start and with previously unseen text.
    const extraText = " I expect about ten minutes.";
    const snapshot = { ...message, content: [{ type: "text", text: text + extraText }] };
    await deliver(update(transport === "delta-only" ? "toolcall_delta" : "toolcall_start", snapshot));
    await expect(status).toContainText("Preparing tool call…");
    await expect(card).toContainText(extraText);
    await expect(card).not.toHaveClass(/message--streaming/);
    await card.locator(".message-body p").evaluate((element) => { window.preparationParagraph = element; });

    const deltaDelivered = page.waitForResponse(async (response) =>
      new URL(response.url()).pathname === "/events" &&
      (await response.json()).events.some((event) => event.assistantMessageEvent?.type === "toolcall_delta"));
    await deliver(update("toolcall_delta", snapshot));
    await deltaDelivered;
    // Allow the Markdown debounce to run if this unchanged text was scheduled again.
    await page.waitForTimeout(300);
    expect(await page.evaluate(() => window.preparationParagraph.isConnected)).toBe(true);
    await expect(status).toContainText("Preparing tool call…");
    await expect(card).not.toHaveClass(/message--streaming/);

    // Continued text should restore ordinary output feedback and its cursor.
    await deliver(update("text_delta", { ...message, content: [{ type: "text", text: text + extraText + " Still working." }] }));
    await expect(status).toContainText("Pi is running…");
    await expect(card).toContainText("Still working.");
    await expect(card).toHaveClass(/message--streaming/);
    await deliver({ type: "message_end", message }, { type: "agent_settled" });
    await expect(status).toHaveAttribute("data-state", "done");
    await expect(card).not.toHaveClass(/message--streaming/);
  });
}

test("tool-only preparation feedback clears at lifecycle boundaries", async ({ page }) => {
  const deliver = await eventDelivery(page);
  const status = page.locator(".composer-state");
  const message = { role: "assistant", content: [] };
  for (const ending of [
    { type: "message_update", assistantMessageEvent: { type: "toolcall_end" }, gatewayPartialMessage: message },
    { type: "message_end", message },
    { type: "tool_execution_start", toolName: "write", toolCallId: "preparation-test", args: { path: "/tmp/example.txt", content: "example" } },
    { type: "turn_end" },
    { type: "agent_settled" },
    { type: "agent_settled", error: "Preparation failed" },
    { type: "compaction_start" },
  ]) {
    await deliver(
      { type: "agent_start" },
      { type: "message_start", message },
      { type: "message_update", assistantMessageEvent: { type: "toolcall_delta", delta: "hidden" }, gatewayPartialMessage: message },
    );
    await expect(status).toContainText("Preparing tool call…");
    await deliver(ending);
    await expect(status).not.toContainText("Preparing tool call…");
    if (ending.type === "compaction_start") {
      await expect(status).toContainText("Compacting…");
      await deliver({ type: "compaction_end", aborted: true });
    }
    await deliver({ type: "agent_start" }, { type: "agent_settled" });
    await expect(status).toHaveAttribute("data-state", "done");
  }
});
