import assert from "node:assert/strict";
import { test } from "node:test";

import { messageFingerprint } from "../public/assets/formatting.js";
import { LiveMessageParser } from "../public/assets/live_message_parser.js";
import { LiveMessageRenderer } from "../public/assets/live_message_renderer.js";
import { ServerMarkdownRenderer } from "../public/assets/server_markdown_renderer.js";
import { FakeDocument, FakeElement, deferred, settle } from "./helpers/fake_dom.mjs";

test("live user messages render and update plain URLs as links", () => {
  const document = new FakeDocument();
  const conversation = {
    followLiveOutput: () => false,
    afterLiveOutputChange() {},
  };
  const renderer = new LiveMessageRenderer(document, conversation, {}, { bind() {} });
  renderer.liveOutput = new FakeElement("div");

  const entry = renderer.appendMessage("user", "Open https://example.test/task/42.");

  const link = entry.body.children.find((child) => child.tagName === "A");
  assert.equal(link?.getAttribute("href"), "https://example.test/task/42");
  assert.equal(link?.getAttribute("target"), "_blank");

  renderer.updateLiveSegment(entry, "user", { compact: false, text: "Use https://example.test/task/43." }, false);
  const updatedLink = entry.body.children.find((child) => child.tagName === "A");
  assert.equal(updatedLink?.getAttribute("href"), "https://example.test/task/43");
});

function markdownBody() {
  return {
    dataset: {},
    innerHTML: "",
    textContent: "",
    closest() { return null; },
  };
}

test("Markdown binding aborts stale work and superseded failures cannot replace current output", async () => {
  const originalFetch = globalThis.fetch;
  const requests = [];
  globalThis.fetch = (_url, options) => {
    const request = deferred();
    requests.push({ ...request, signal: options.signal });
    return request.promise;
  };
  try {
    const renderer = new ServerMarkdownRenderer({}, { autoScrollEnabled: false });
    const staleBody = markdownBody();
    renderer.render(staleBody, "old", 0);
    await settle(() => requests.length === 1);
    renderer.bind();
    assert.equal(requests[0].signal.aborted, true);
    requests[0].resolve({ ok: true, json: async () => ({ html: "<strong>stale</strong>" }) });
    await new Promise((resolve) => setTimeout(resolve, 0));
    assert.equal(staleBody.innerHTML, "");

    const body = markdownBody();
    renderer.render(body, "first", 0);
    await settle(() => requests.length === 2);
    renderer.render(body, "second", 0);
    await settle(() => requests.length === 3);
    assert.equal(requests[1].signal.aborted, true);
    requests[1].reject(new Error("superseded failure"));
    requests[2].resolve({ ok: false });
    await settle(() => body.dataset.rendering === undefined);
    assert.equal(body.textContent, "second");
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("live parser preserves representative SSR shapes and renderer deduplicates persisted messages", () => {
  const parser = new LiveMessageParser("/home/tester");
  const assistant = parser.contentSegments([{ type: "text", text: "Answer" }], { role: "assistant" });
  const thinking = parser.contentSegments([{ type: "thinking", thinking: "Consider" }], { role: "assistant" });
  const tool = parser.contentSegments([{ type: "toolCall", name: "read", id: "r1", arguments: { path: "/home/tester/a" } }], { role: "assistant" });
  const subagent = parser.contentSegments([{ type: "text", text: "Done" }], { role: "toolResult", toolName: "subagent", toolCallId: "s1", details: { task: "Review" } });
  const user = parser.contentSegments([{ type: "text", text: "Image" }, { type: "image", mimeType: "image/png", data: "cG5n" }], { role: "user" });

  assert.equal(assistant[0].text, "Answer");
  assert.equal(thinking[0].thinking, true);
  assert.deepEqual(tool[0].summaryParts, { name: "read", path: "~/a", range: "" });
  assert.equal(subagent[0].toolPrompt, "Review");
  assert.equal(user[0].images[0].src, "data:image/png;base64,cG5n");

  const timestamp = "2026-01-01T00:00:00.000Z";
  const persisted = { dataset: { messageFingerprint: messageFingerprint("assistant", "Answer", timestamp) } };
  const renderer = new LiveMessageRenderer({}, { element: {
    querySelectorAll(selector) {
      assert.equal(selector, ".message:not(.message--live)[data-message-fingerprint]");
      return [persisted];
    },
  } }, parser, { bind() {} });
  renderer.conversationScroll = renderer.conversationController.element;
  assert.equal(renderer.liveMessageAlreadyRendered("assistant", "Answer", timestamp), true);
  assert.equal(renderer.liveMessageAlreadyRendered("assistant", "Different", timestamp), false);
});

test("compact tool summaries render a hidden Show toggle, compaction summaries do not", () => {
  const document = new FakeDocument();
  const conversation = {
    followLiveOutput: () => false,
    afterLiveOutputChange() {},
  };
  const renderer = new LiveMessageRenderer(document, conversation, new LiveMessageParser("/home/tester"), { bind() {} });
  renderer.liveOutput = new FakeElement("div");

  const tool = renderer.appendCompactMessage("assistant", "$ pi --no-session -p review", "", false, false, null, { toolName: "bash" });
  const toggle = tool.details.querySelectorAll("button").find((button) => button.classList.contains("tool-summary-toggle"));
  assert.equal(toggle?.textContent, "Show");
  assert.equal(toggle?.hidden, true);
  assert.equal(toggle?.getAttribute("aria-expanded"), "false");
  assert.equal(toggle?.parentElement.classList.contains("message-details-summary"), true);

  const compaction = renderer.appendCompactMessage("status", "Compacted", "", false, false, null, { compaction: true });
  const compactionToggle = compaction.details.querySelectorAll("button").find((button) => button.classList.contains("tool-summary-toggle"));
  assert.equal(compactionToggle, undefined);
});

test("tool lifecycle summary changes refresh only their clamp visibility", () => {
  const document = new FakeDocument();
  const refreshed = [];
  const conversation = {
    followLiveOutput: () => false,
    afterLiveOutputChange() {},
    refreshToolSummaryToggle(summary) { refreshed.push(summary); },
  };
  const renderer = new LiveMessageRenderer(document, conversation, new LiveMessageParser("/home/tester"), { bind() {} });
  renderer.liveOutput = new FakeElement("div");
  renderer.renderToolTranscriptBody = () => {};
  renderer.replaceMessageImages = () => {};
  renderer.markLiveEntryRendered = () => true;

  renderer.renderToolExecutionEvent({ type: "tool_execution_start", toolCallId: "call-1", toolName: "search" });
  const tool = renderer.liveToolExecutions.get("call-1");
  assert.deepEqual(refreshed, [tool.summaryText.parentElement]);

  refreshed.length = 0;
  renderer.renderToolExecutionEvent({ type: "tool_execution_update", toolCallId: "call-1", toolName: "search" });
  assert.deepEqual(refreshed, []);

  renderer.renderToolExecutionEvent({ type: "tool_execution_end", toolCallId: "call-1", toolName: "search", result: { content: [{ type: "text", text: "done" }] } });
  assert.deepEqual(refreshed, [tool.summaryText.parentElement]);

  refreshed.length = 0;
  renderer.renderMessageEvent({
    type: "message_end",
    message: { role: "toolResult", toolCallId: "call-1", toolName: "search", content: [{ type: "text", text: "done" }], timestamp: 1_767_225_600_000 },
  });
  assert.deepEqual(refreshed, [tool.summaryText.parentElement]);

  refreshed.length = 0;
  renderer.renderBashEvent({ type: "bash_start", bashId: "bash-1", command: "printf before" });
  assert.equal(refreshed.length, 1);

  refreshed.length = 0;
  const bash = renderer.renderBashEvent({ type: "bash_end", bashId: "bash-1", command: "printf after", result: { output: "" } });
  assert.deepEqual(refreshed, [bash.summaryText.parentElement]);
});

test("terminal updates coalesce to the latest screen and stale bindings do not render", async () => {
  const changes = [];
  const conversation = {
    followLiveOutput: () => false,
    afterLiveOutputChange: () => changes.push("changed"),
  };
  const renderer = new LiveMessageRenderer({}, conversation, {}, { bind() {} });
  const rendered = [];
  renderer.renderResolvedToolTranscriptBody = (_body, lines, rawText) => rendered.push({ lines, rawText });
  const body = {};

  renderer.queueTerminalTranscriptRender(body, "old 10%\rold 20%", "bash", {});
  renderer.queueTerminalTranscriptRender(body, "new 10%\rnew 90%", "bash", {});
  await settle(() => renderer.terminalRenderStates.get(body)?.rendering === false);
  assert.deepEqual(rendered.map(({ rawText }) => rawText), ["new 90%"]);
  assert.equal(changes.length, 1);

  const staleBody = {};
  renderer.queueTerminalTranscriptRender(staleBody, "stale 10%\rstale 90%", "bash", {});
  renderer.terminalBindingGeneration += 1;
  await settle(() => renderer.terminalRenderStates.get(staleBody)?.rendering === false);
  assert.equal(rendered.length, 1);
});
