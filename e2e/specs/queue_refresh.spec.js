import { expect, test } from "@playwright/test";
import { sessions } from "../support/contract.mjs";
import { selectSession } from "../support/ui.mjs";
import {
  activateClearQueue, attachClearQueueDraft, expectClearedQueue, expectClearQueueDraft,
  expectClearQueueRunning, prepareClearQueue, queuedSteering, stopClearQueueRun
} from "../support/clear_queue.mjs";

test.afterEach(async ({ page }) => stopClearQueueRun(page));

test("clear snapshot updates the queue without polling and stale polls cannot resurrect it", async ({ page }) => {
  await prepareClearQueue(page, sessions.clearQueue);
  await attachClearQueueDraft(page);
  const polls = await controlQueuePolls(page);
  const response = page.waitForResponse("**/clear_queue");
  await activateClearQueue(page);
  const snapshot = await (await response).json();
  await expectClearedQueue(page);
  await expectClearQueueDraft(page);

  // HTTP reconciliation must not skip tool/message events on the next poll.
  await polls.deliver([], polls.cursor);
  expect(polls.cursor).toBeLessThan(snapshot.event_sequence);
  await polls.deliver([{ type: "queue_update", steering: [queuedSteering], followUp: [] }], snapshot.event_sequence - 1);
  await expectClearedQueue(page);
  await polls.deliver([{ type: "queue_update", steering: [], followUp: ["newer queued message"] }], snapshot.event_sequence + 1);
  await expect(page.locator(".pending-message")).toHaveText(["Follow-up: newer queued message"]);
  await expectClearQueueDraft(page);
});

test("late clear snapshots preserve newer queues and snapshots can contain concurrent messages", async ({ page }) => {
  await prepareClearQueue(page, sessions.clearQueue);
  const polls = await controlQueuePolls(page);
  const sequence = polls.cursor + 100;
  let release;
  const held = new Promise((resolve) => { release = resolve; });
  await page.route("**/clear_queue", async (route) => {
    await held;
    await route.fulfill({ json: { ok: true, queued_messages: {}, event_sequence: sequence } });
  });
  try {
    const request = page.waitForRequest("**/clear_queue");
    const response = page.waitForResponse("**/clear_queue");
    await activateClearQueue(page);
    await request;
    await polls.deliver([{ type: "queue_update", steering: [], followUp: ["newer queued message"] }], sequence + 1);
    await expect(page.locator(".pending-message")).toHaveText(["Follow-up: newer queued message"]);
    release();
    await (await response).finished();
    await painted(page);
    await expect(page.locator(".pending-message")).toHaveText(["Follow-up: newer queued message"]);

    await page.unroute("**/clear_queue");
    await page.route("**/clear_queue", (route) => route.fulfill({ json: {
      ok: true, event_sequence: sequence + 2, queued_messages: { followUp: ["queued during clear"] }
    } }));
    await activateClearQueue(page);
    await expect(page.locator(".pending-message")).toHaveText(["Follow-up: queued during clear"]);
    await expectClearQueueRunning(page);
  } finally {
    release();
  }
});

test("subagent updates without messages do not block queue or subsequent message rendering", async ({ page }) => {
  await prepareClearQueue(page, sessions.clearQueue);
  const polls = await controlQueuePolls(page);
  await polls.deliver([
    {
      type: "tool_execution_update", toolCallId: "running-subagent", toolName: "subagent",
      partialResult: { content: [], details: { mode: "single", results: [{ agent: "worker", exitCode: -1, messages: null }] } }
    },
    { type: "queue_update", steering: ["intermediate queue"], followUp: [] },
    { type: "queue_update", steering: [], followUp: [] },
    { type: "message_end", message: { role: "assistant", content: [{ type: "text", text: "Response after subagent update" }] } }
  ], polls.cursor + 4);
  await expectClearedQueue(page);
  await expect(page.locator('article[data-role="assistant"]').filter({ hasText: "Response after subagent update" })).toBeVisible();
});

test("late clear snapshots cannot update another session", async ({ page }) => {
  await prepareClearQueue(page, sessions.clearQueue);
  const polls = await controlQueuePolls(page);
  await polls.deliver([{ type: "queue_update", followUp: ["before switching"] }], 10000);
  let release;
  const held = new Promise((resolve) => { release = resolve; });
  await page.route("**/clear_queue", async (route) => {
    await held;
    await route.fulfill({ json: {
      ok: true, event_sequence: 10001, queued_messages: { followUp: ["wrong session"] }
    } });
  });
  try {
    const request = page.waitForRequest("**/clear_queue");
    const response = page.waitForResponse("**/clear_queue");
    await activateClearQueue(page);
    await request;
    await selectSession(page, sessions.controlsAbort);
    release();
    await (await response).finished();
    await painted(page);
    await expect(page.locator(".pending-message")).toHaveCount(0);
    // Returning to the original session must reset queue ordering too.
    await selectSession(page, sessions.clearQueue);
    await page.unroute("**/clear_queue");
    await page.route("**/clear_queue", (route) => route.fulfill({ json: {
      ok: true, event_sequence: 9999, queued_messages: { followUp: ["current session"] }
    } }));
    await activateClearQueue(page);
    await expect(page.locator(".pending-message")).toHaveText(["Follow-up: current session"]);
  } finally {
    release();
  }
});

test("same-session refresh invalidates a held clear snapshot from the previous queue baseline", async ({ page }) => {
  await prepareClearQueue(page, sessions.clearQueue);
  const polls = await controlQueuePolls(page);
  let release;
  const held = new Promise((resolve) => { release = resolve; });
  await page.route("**/clear_queue", async (route) => {
    await held;
    await route.fulfill({ json: {
      ok: true, event_sequence: 10000, queued_messages: { followUp: ["obsolete queued message"] }
    } });
  });
  await page.route(/\/session_fragment(?:\?|$)/, async (route) => {
    const response = await route.fetch();
    const payload = await response.json();
    payload.conversation_html = await page.evaluate((html) => {
      const template = document.createElement("template");
      template.innerHTML = html;
      Object.assign(template.content.querySelector("#live-output").dataset, {
        queuedMessages: "{}", eventsAfter: "0", sessionSyncMode: "external_follow", sessionSyncRevision: "refreshed"
      });
      return template.innerHTML;
    }, payload.conversation_html);
    await route.fulfill({ json: payload });
  });
  try {
    const request = page.waitForRequest("**/clear_queue");
    const response = page.waitForResponse("**/clear_queue");
    await activateClearQueue(page);
    await request;
    await polls.deliver([], polls.cursor, { mode: "external_follow", revision: "refreshed" });
    await expect(page.locator("#live-output")).toHaveAttribute("data-session-sync-revision", "refreshed");
    await expect(page.locator(".pending-message")).toHaveCount(0);
    release();
    await (await response).finished();
    await painted(page);
    await expect(page.locator(".pending-message")).toHaveCount(0);
  } finally {
    release();
  }
});

async function painted(page) {
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
}

async function controlQueuePolls(page) {
  let pending;
  let ready;
  const firstPoll = new Promise((resolve) => { ready = resolve; });
  const polls = {
    cursor: 0,
    async deliver(events, sequence, sessionSync) {
      const delivered = new Promise((resolve) => { pending = { events, sequence, sessionSync, resolve }; });
      await delivered;
      await painted(page);
    }
  };
  await page.route(/\/events(?:\?|$)/, async (route) => {
    polls.cursor = Number(new URL(route.request().url()).searchParams.get("after"));
    const batch = pending;
    pending = null;
    await route.fulfill({ json: {
      events: batch?.events || [], last_seq: batch?.sequence ?? polls.cursor, missed: false, session_sync: batch?.sessionSync
    } });
    ready();
    batch?.resolve();
  });
  await firstPoll;
  return polls;
}
