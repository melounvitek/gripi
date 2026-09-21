import { expect, test } from "@playwright/test";

test.use({ hasTouch: true });

async function eventDelivery(page) {
  const history = [];
  page.on("framenavigated", (frame) => {
    if (frame === page.mainFrame()) history.length = 0;
  });
  await page.route(/\/events(?:\?|$)/, async (route) => {
    // Like the gateway, replay unacknowledged events after an interrupted poll.
    const after = Number(new URL(route.request().url()).searchParams.get("after") || 0);
    await route.fulfill({ json: { events: history.slice(after), last_seq: history.length, missed: false } });
  });
  await page.goto("/");
  await expect(page.locator("#live-output")).toBeAttached();
  return async (...events) => {
    history.push(...events);
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
    const preparation = page.locator(".message--tool-preparation");
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
    await expect(status).toContainText("Pi is running…");
    await expect(card).toContainText(extraText);
    await expect(card).not.toHaveClass(/message--streaming/);
    await expect(preparation).toHaveCount(1);
    await expect(preparation).toBeVisible();
    await expect(preparation.locator(".role")).toHaveText("pi");
    await expect(preparation.locator(".message-body")).toHaveText("Preparing tool call…");
    await expect(preparation.locator(".message-body")).toHaveCSS("font-style", "italic");
    await expect(preparation).not.toHaveClass(/message--streaming/);
    expect(await card.evaluate((element) => Boolean(element.compareDocumentPosition(document.querySelector(".message--tool-preparation")) & Node.DOCUMENT_POSITION_FOLLOWING))).toBe(true);
    await preparation.evaluate((element) => { window.preparationCard = element; });
    await card.locator(".message-body p").evaluate((element) => { window.preparationParagraph = element; });

    const deltaDelivered = page.waitForResponse(async (response) =>
      new URL(response.url()).pathname === "/events" &&
      (await response.json()).events.some((event) => event.assistantMessageEvent?.type === "toolcall_delta"));
    await deliver(update("toolcall_delta", snapshot));
    await deltaDelivered;
    // Allow the Markdown debounce to run if this unchanged text was scheduled again.
    await page.waitForTimeout(300);
    expect(await page.evaluate(() => window.preparationParagraph.isConnected && window.preparationCard.isConnected)).toBe(true);
    await expect(preparation).toHaveCount(1);
    await expect(status).toContainText("Pi is running…");
    await expect(card).not.toHaveClass(/message--streaming/);

    const activityToggle = page.getByRole("switch", { name: "Show agent activity" });
    await activityToggle.tap();
    await expect(preparation).toBeHidden();
    await expect(status).toContainText("Pi is running…");
    await expect(page.locator(".focus-activity-summary").last()).toContainText("1 other update");
    await activityToggle.tap();
    await expect(preparation).toBeVisible();
    await preparation.screenshot({ path: test.info().outputPath("tool-preparation-card.png") });

    await page.keyboard.press("Control+f");
    await page.getByRole("searchbox", { name: "Find in conversation" }).fill("Preparing tool call");
    await expect(page.locator("[data-current-session-find-count]")).toHaveText("1 / 1");
    await page.getByRole("checkbox", { name: "Conversation only" }).check();
    await expect(page.locator("[data-current-session-find-count]")).toHaveText("0 / 0");
    await page.getByRole("button", { name: "Close find" }).click();

    // Continued text should restore ordinary output feedback and its cursor.
    await deliver(update("text_delta", { ...message, content: [{ type: "text", text: text + extraText + " Still working." }] }));
    await expect(status).toContainText("Pi is running…");
    await expect(card).toContainText("Still working.");
    await expect(preparation).toHaveCount(0);
    await expect(card).toHaveClass(/message--streaming/);
    await deliver({ type: "message_end", message }, { type: "agent_settled" });
    await expect(status).toHaveAttribute("data-state", "done");
    await expect(card).not.toHaveClass(/message--streaming/);
  });
}

test("preparation yields to the current cumulative tool card, not an earlier tool", async ({ page }) => {
  const deliver = await eventDelivery(page);
  const earlierTool = { type: "toolCall", id: "earlier-tool", name: "write", arguments: { path: "/tmp/earlier.txt", content: "earlier" } };
  const currentTool = { type: "toolCall", id: "current-tool", name: "write", arguments: { path: "/tmp/current.txt", content: "partial" } };
  const update = (content) => ({
    type: "message_update",
    assistantMessageEvent: { type: "toolcall_delta", contentIndex: 1, delta: "fragment" },
    message: { role: "assistant", content },
  });
  await deliver({ type: "agent_start" }, update([earlierTool]));
  const preparation = page.locator(".message--tool-preparation");
  await expect(preparation).toBeVisible();
  await expect(page.locator('[data-tool-call-id="earlier-tool"]')).toBeVisible();

  await deliver(update([earlierTool, currentTool]));
  await expect(page.locator('[data-tool-call-id="current-tool"]')).toBeVisible();
  await expect(preparation).toHaveCount(0);
  await expect(page.locator(".composer-state")).toContainText("Pi is running…");
});

test("cumulative subagent preparation stays visible until its execution card appears", async ({ page }) => {
  const deliver = await eventDelivery(page);
  const toolCall = { type: "toolCall", id: "preparing-subagent", name: "subagent", arguments: { task: "Review this change" } };
  await deliver(
    { type: "agent_start" },
    { type: "message_update", assistantMessageEvent: { type: "toolcall_delta", contentIndex: 0 }, message: { role: "assistant", content: [toolCall] } },
  );
  const preparation = page.locator(".message--tool-preparation");
  await expect(preparation).toBeVisible();
  await deliver({ type: "tool_execution_start", toolCallId: toolCall.id, toolName: toolCall.name, args: toolCall.arguments });
  await expect(page.locator('[data-tool-call-id="preparing-subagent"]')).toBeVisible();
  await expect(preparation).toHaveCount(0);
});

test("tool-only preparation feedback clears at lifecycle boundaries", async ({ page }) => {
  const deliver = await eventDelivery(page);
  const status = page.locator(".composer-state");
  const message = { role: "assistant", content: [] };
  const preparation = page.locator(".message--tool-preparation");
  const toolCall = { type: "toolCall", id: "preparation-test", name: "write", arguments: { path: "/tmp/example.txt", content: "example" } };
  for (const ending of [
    { type: "message_start", message },
    { type: "agent_start" },
    { type: "message_update", assistantMessageEvent: { type: "toolcall_end", toolCall }, gatewayPartialMessage: { ...message, content: [toolCall] } },
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
    await expect(status).toContainText("Pi is running…");
    await expect(preparation).toHaveCount(1);
    await expect(preparation).toBeVisible();
    await deliver(ending);
    await expect(preparation).toHaveCount(0);
    if (ending.type === "message_update") {
      await expect(page.locator('[data-tool-call-id="preparation-test"]')).toBeVisible();
    }
    if (ending.type === "compaction_start") {
      await expect(status).toContainText("Compacting…");
      await deliver({ type: "compaction_end", aborted: true });
    }
    await deliver({ type: "agent_start" }, { type: "agent_settled" });
    await expect(status).toHaveAttribute("data-state", "done");
  }
});

test("preparation completes after its completion poll is interrupted", async ({ page }) => {
  await page.addInitScript(() => {
    const fetch = window.fetch.bind(window);
    let interrupted = false;
    window.fetch = async (...args) => {
      const response = await fetch(...args);
      if (!interrupted && new URL(response.url).pathname === "/events") {
        const payload = await response.clone().json();
        if (payload.events.some((event) => event.type === "agent_settled")) {
          interrupted = true;
          throw new DOMException("Simulated interrupted poll", "AbortError");
        }
      }
      return response;
    };
  });
  const deliver = await eventDelivery(page);
  await deliver(
    { type: "agent_start" },
    { type: "message_update", assistantMessageEvent: { type: "toolcall_delta", contentIndex: 0 }, gatewayPartialMessage: { role: "assistant", content: [] } },
  );
  await expect(page.locator(".message--tool-preparation")).toBeVisible();
  await deliver({ type: "agent_settled" });
  await expect(page.locator(".composer-state")).toHaveAttribute("data-state", "done");
  await expect(page.locator(".message--tool-preparation")).toHaveCount(0);
});

for (const boundary of ["reload", "abort", "forced abort"]) {
  test(`preparation card clears on ${boundary}, including while activity is hidden`, async ({ page }) => {
    const deliver = await eventDelivery(page);
    const message = { role: "assistant", content: [] };
    const preparation = page.locator(".message--tool-preparation");
    const status = page.locator(".composer-state");
    await deliver(
      { type: "agent_start" },
      { type: "message_start", message },
      { type: "message_update", assistantMessageEvent: { type: "toolcall_delta", delta: "hidden" }, gatewayPartialMessage: message },
    );
    await expect(preparation).toBeVisible();
    await page.getByRole("switch", { name: "Show agent activity" }).tap();
    await expect(preparation).toBeHidden();
    await expect(status).toContainText("Pi is running…");

    if (boundary === "reload") {
      await page.reload();
      await expect(page.locator("#live-output")).toBeAttached();
    } else {
      await page.route("**/abort", async (route) => {
        await route.fulfill({ json: { forced: boundary === "forced abort", editorText: "" } });
      });
      await page.getByRole("button", { name: "Abort running Pi" }).tap();
      await expect(page.locator("body")).not.toHaveClass(/session-switching/);
      if (boundary === "abort") await deliver({ type: "agent_settled" });
      await expect(status).toHaveAttribute("data-state", "done");
    }
    await expect(preparation).toHaveCount(0);
    const toggle = page.getByRole("switch", { name: "Show agent activity" });
    if (await toggle.getAttribute("aria-checked") === "false") await toggle.tap();
    await expect(preparation).toHaveCount(0);
  });
}
