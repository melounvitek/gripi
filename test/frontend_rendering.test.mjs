import assert from "node:assert/strict";
import { test } from "node:test";

import { eventTimestamp, messageFingerprint } from "../public/assets/formatting.js";
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

test("live message images render as accessible viewer buttons", () => {
  const document = new FakeDocument();
  const conversation = {
    followLiveOutput: () => false,
    afterLiveOutputChange() {},
  };
  const renderer = new LiveMessageRenderer(document, conversation, {}, { bind() {} });
  renderer.liveOutput = new FakeElement("div");

  const entry = renderer.appendMessage("user", "Screenshot", true, false, null, {
    images: [{ src: "data:image/png;base64,cG5n", alt: "Screenshot" }],
  });

  const container = entry.article.querySelector(".message-images");
  const button = container.children[0];
  const image = button.children[0];
  assert.equal(button.tagName, "BUTTON");
  assert.equal(button.type, "button");
  assert.equal(button.dataset.imageViewerOpen, "");
  assert.equal(button.getAttribute("aria-label"), "View screenshot full size");
  assert.equal(image.tagName, "IMG");
  assert.equal(image.src, "data:image/png;base64,cG5n");
  assert.equal(image.alt, "Screenshot");
});

test("live image object URLs are released when their messages leave the conversation", () => {
  const document = new FakeDocument();
  const renderer = new LiveMessageRenderer(document, {
    followLiveOutput: () => false,
    afterLiveOutputChange() {},
  }, {}, { bind() {} });
  renderer.liveOutput = new FakeElement("div");
  const entry = renderer.appendMessage("user", "Screenshot", true, false, null, {
    images: [{ src: "blob:image-preview", alt: "Screenshot" }],
  });
  const revoked = [];
  const originalRevoke = URL.revokeObjectURL;
  URL.revokeObjectURL = (url) => revoked.push(url);

  try {
    renderer.releaseMessageImageObjectURLs(entry.article);
  } finally {
    URL.revokeObjectURL = originalRevoke;
  }

  assert.deepEqual(revoked, ["blob:image-preview"]);
  assert.equal(entry.article.querySelector(".message-image").dataset.objectUrl, undefined);
});

test("delta-only updates use the gateway partial message and its timestamp", () => {
  const parser = new LiveMessageParser();
  const gatewayPartialMessage = {
    role: "assistant",
    content: [{ type: "thinking", thinking: "Live reasoning" }],
    timestamp: "2026-08-07T08:26:06.648Z",
  };
  const event = {
    type: "message_update",
    assistantMessageEvent: { type: "thinking_delta", contentIndex: 0, delta: " reasoning" },
    gatewayPartialMessage,
  };

  assert.equal(parser.eventMessage(event), gatewayPartialMessage);
  assert.equal(eventTimestamp(event), gatewayPartialMessage.timestamp);
});

function markdownBody() {
  return {
    dataset: {},
    innerHTML: "",
    textContent: "",
    closest() { return null; },
  };
}

test("authoritative assistant endings remove provisional segments they omit", () => {
  const conversation = {
    element: new FakeElement("section"),
    followLiveOutput: () => false,
    afterLiveOutputChange() {},
  };
  const renderer = new LiveMessageRenderer({}, conversation, new LiveMessageParser(), { bind() {} });
  renderer.conversationScroll = conversation.element;
  const article = new FakeElement("article");
  conversation.element.append(article);
  renderer.liveAssistantSegments.set("0-text", { article, compact: false });

  renderer.renderMessageEvent({ type: "message_end", message: { role: "assistant", content: [] } });

  assert.equal(article.parentElement, null);
  assert.equal(renderer.liveAssistantSegments.size, 0);
});

test("live thinking follows the bound Pi display setting through streaming updates", () => {
  const document = new FakeDocument();
  const conversation = {
    element: new FakeElement("section"),
    followLiveOutput: () => false,
    afterLiveOutputChange() {},
  };
  let output = new FakeElement("section");
  output.dataset.hideThinkingBlock = "true";
  document.getElementById = () => output;
  const rendered = [];
  const markdown = {
    bind() {},
    render(body, text) {
      rendered.push(text);
      body.textContent = text;
    },
  };
  const renderer = new LiveMessageRenderer(document, conversation, {}, markdown);

  renderer.bind();
  const entry = renderer.appendMessage("assistant", "Private reasoning", true, false, null, { thinking: true });
  assert.equal(entry.body.textContent, "Thinking...");
  assert.deepEqual(rendered, ["Thinking..."]);

  renderer.updateLiveSegment(entry, "assistant", { compact: false, thinking: true, text: "Longer private reasoning" }, false);
  assert.equal(entry.body.textContent, "Thinking...");
  assert.deepEqual(rendered, ["Thinking...", "Thinking..."]);

  output = new FakeElement("section");
  output.dataset.hideThinkingBlock = "false";
  renderer.bind();
  const visible = renderer.appendMessage("assistant", "Visible reasoning", true, false, null, { thinking: true });
  assert.equal(visible.body.textContent, "Visible reasoning");
});

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
  assert.equal(parser.toolExecutionText({ type: "tool_execution_end", toolName: "subagent", result: {}, isError: true }), "(failed)");
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

test("live parser retains complete write content for standard output collapsing", () => {
  const parser = new LiveMessageParser();
  const content = Array.from({ length: 24 }, (_, index) => `write-line-${index + 1}`).join("\n");

  const [segment] = parser.contentSegments([{
    type: "toolCall",
    id: "write-1",
    name: "write",
    arguments: { path: "output.txt", content },
  }], { role: "assistant" });

  assert.equal(segment.text.split("\n").length, 24);
  assert.match(segment.text, /^\+ write-line-1\n/);
  assert.match(segment.text, /\+ write-line-24$/);
});

test("restored subagents prefer gateway timestamps and retain persisted-call fallback", () => {
  const renderer = new LiveMessageRenderer({}, {}, {}, { bind() {} });
  renderer.liveOutput = { dataset: {
    activeToolEvents: JSON.stringify([
      { type: "tool_execution_start", toolCallId: "current", toolName: "subagent", gatewayTimestamp: "2026-01-01T10:00:00Z" },
      { type: "tool_execution_start", toolCallId: "legacy", toolName: "subagent" },
    ]),
    activeToolTimestamps: JSON.stringify({ current: "2025-01-01T10:00:00Z", legacy: "2026-01-01T11:00:00Z" }),
    activeToolPrompts: "{}",
  } };
  const restored = [];
  renderer.renderToolExecutionEvent = (event, timestamp, fallback) => restored.push({ id: event.toolCallId, timestamp, fallback });

  renderer.restoreActiveToolExecutions();

  assert.deepEqual(restored, [
    { id: "current", timestamp: "2026-01-01T10:00:00Z", fallback: false },
    { id: "legacy", timestamp: "2026-01-01T11:00:00Z", fallback: false },
  ]);
});

test("canonical subagent timestamps replace provisional card timestamps", () => {
  const document = new FakeDocument();
  const conversation = {
    element: new FakeElement("section"),
    followLiveOutput: () => false,
    afterLiveOutputChange() {},
  };
  const renderer = new LiveMessageRenderer(document, conversation, new LiveMessageParser(), { bind() {} });
  renderer.liveOutput = new FakeElement("section");
  renderer.conversationScroll = conversation.element;
  renderer.renderToolExecutionEvent({ type: "tool_execution_start", toolCallId: "subagent-1", toolName: "subagent" }, "2025-01-01T10:00:00Z", false);

  const entry = renderer.liveToolExecutions.get("subagent-1");
  assert.equal(entry.article.dataset.messageTimestamp, String(Date.parse("2025-01-01T10:00:00Z") / 1000));

  renderer.renderToolExecutionEvent({ type: "tool_execution_update", toolCallId: "subagent-1", toolName: "subagent", gatewayTimestamp: "2026-01-01T10:00:00Z", partialResult: {} });

  assert.equal(entry.article.dataset.messageTimestamp, String(Date.parse("2026-01-01T10:00:00Z") / 1000));
  assert.match(entry.article.dataset.messageFingerprint, /^tool:1767261600:/);
});

test("persisted tool replay suppression remains bounded", () => {
  const renderer = new LiveMessageRenderer({}, {}, {}, { bind() {} });
  for (let index = 0; index < 20; index += 1) renderer.rememberPersistedToolResult(`tool-${index}`);

  assert.equal(renderer.persistedToolResultIDs.size, 16);
  assert.equal(renderer.persistedToolResultIDs.has("tool-0"), false);
  assert.equal(renderer.persistedToolResultIDs.has("tool-19"), true);
});

test("paged persisted subagents replace matching live cards and suppress replay", () => {
  const document = new FakeDocument();
  const conversation = {
    element: new FakeElement("section"),
    followLiveOutput: () => false,
    afterLiveOutputChange() {},
    scheduleFocusedActivityRefresh() {},
  };
  const renderer = new LiveMessageRenderer(document, conversation, new LiveMessageParser(), { bind() {} });
  renderer.liveOutput = new FakeElement("section");
  conversation.element.append(renderer.liveOutput);
  renderer.conversationScroll = conversation.element;
  renderer.renderToolExecutionEvent({ type: "tool_execution_start", toolCallId: "matching", toolName: "subagent" });
  renderer.renderToolExecutionEvent({ type: "tool_execution_start", toolCallId: "unrelated", toolName: "other" });
  const matching = renderer.liveToolExecutions.get("matching");
  const unrelated = renderer.liveToolExecutions.get("unrelated");
  renderer.resetLiveAssistantTracking();
  conversation.element.querySelectorAll = (selector) => selector === ".message--live[data-tool-call-id]" ? [matching.article, unrelated.article] : [];
  const persisted = { dataset: { role: "toolResult", toolCallId: "matching" } };
  const unrelatedPersisted = { dataset: { role: "toolResult", toolCallId: "historical-other" } };
  const history = {
    querySelectorAll(selector) {
      assert.equal(selector, ".message:not(.message--live)[data-tool-call-id]");
      return [persisted, unrelatedPersisted];
    },
  };

  renderer.reconcilePersistedToolResults(history);

  assert.equal(matching.article.parentElement, null);
  assert.equal(renderer.liveToolExecutions.has("matching"), false);
  assert.equal(unrelated.article.parentElement, renderer.liveOutput);
  renderer.renderToolExecutionEvent({ type: "tool_execution_update", toolCallId: "matching", toolName: "subagent", partialResult: {} });
  renderer.renderMessageEvent({ type: "message_end", message: { role: "toolResult", toolCallId: "matching", toolName: "subagent", content: [{ type: "text", text: "Done" }] } });
  assert.equal(renderer.liveToolExecutions.has("matching"), false);
  assert.deepEqual(renderer.liveOutput.children, [unrelated.article]);
});

test("persisted tool results ignore replayed message events with the same tool identity", () => {
  const persisted = { dataset: { role: "toolResult", toolCallId: "subagent-1" } };
  const conversation = {
    element: {
      querySelectorAll(selector) {
        if (selector === ".message:not(.message--live)[data-tool-call-id]") return [persisted];
        if (selector === ".message:not(.message--live)[data-message-fingerprint]") return [];
        return [];
      },
    },
    followLiveOutput: () => false,
  };
  const renderer = new LiveMessageRenderer({}, conversation, new LiveMessageParser("/home/tester"), { bind() {} });
  renderer.conversationScroll = conversation.element;
  let appended = 0;
  renderer.appendCompactMessage = () => { appended += 1; };

  renderer.renderMessageEvent({ type: "message_end", message: { role: "toolResult", toolCallId: "subagent-1", toolName: "subagent", content: [{ type: "text", text: "Done" }] } });

  assert.equal(appended, 0);
});

test("result-first general subagent output keeps its transcript display", () => {
  const conversation = {
    element: { querySelectorAll: () => [] },
    followLiveOutput: () => false,
  };
  const parser = new LiveMessageParser();
  const renderer = new LiveMessageRenderer({}, conversation, parser, { bind() {} });
  renderer.conversationScroll = conversation.element;
  let rendered;
  renderer.appendCompactMessage = (role, summary, text, _live, _scroll, _timestamp, options) => {
    rendered = { role, summary, text, options };
  };
  const details = {
    status: "done",
    tools: [{ name: "read", status: "done", args: { path: "file.txt" }, output: "contents" }],
    textItems: ["review complete"],
    usage: { turns: 1 },
  };

  renderer.renderMessageEvent({
    type: "message_end",
    message: {
      role: "toolResult",
      toolCallId: "general-1",
      toolName: "subagent",
      content: [{ type: "text", text: "review complete" }],
      details,
    },
  });

  assert.equal(rendered.role, "toolResult");
  assert.equal(rendered.summary, "subagent general");
  assert.match(rendered.text, /^✓ general\n✓ read file\.txt\n  contents/);
  assert.equal(rendered.options.toolTranscript, true);
});

test("live long single-line tool output collapses to its latest suffix", () => {
  const fragment = () => ({
    childNodes: [],
    replaceChildren(...nodes) { this.childNodes = nodes; },
    cloneNode() { return { childNodes: [...this.childNodes] }; },
  });
  const fullTemplate = { content: fragment() };
  const tailTemplate = { content: fragment() };
  const control = { hidden: true };
  const collapse = {
    dataset: { toolOutputCollapsible: "true", collapsed: "false" },
    hidden: false,
    querySelector(selector) {
      return {
        "[data-tool-output-full]": fullTemplate,
        "[data-tool-output-tail]": tailTemplate,
        "[data-tool-output-collapse-control]": control,
        ".tool-output-hidden-count--desktop": { textContent: "" },
        ".tool-output-hidden-count--mobile": { textContent: "" },
      }[selector];
    },
  };
  const body = {
    dataset: {},
    classList: { toggle() {} },
    closest: () => collapse,
    replaceChildren(...nodes) { this.children = nodes; },
  };
  const renderer = Object.create(LiveMessageRenderer.prototype);
  renderer.parser = { displayHomePath: (text) => text };
  renderer.document = {
    createElement: () => ({
      children: [],
      className: "",
      textContent: "",
      classList: { add() {} },
      append(...nodes) { this.children.push(...nodes); },
    }),
  };
  const output = `oldest-output-${"x".repeat(2500)}-latest-output`;

  renderer.renderToolTranscriptBody(body, output, "bash");

  assert.equal(collapse.dataset.collapsed, "true");
  assert.equal(control.hidden, false);
  assert.doesNotMatch(body.children[0].children[0].textContent, /oldest-output/);
  assert.match(body.children[0].children.at(-1).textContent, /latest-output$/);
  assert.match(fullTemplate.content.childNodes[0].children[0].textContent, /^oldest-output/);
});

test("live general subagent output uses the non-wrapping transcript policy", () => {
  const conversation = { afterLiveOutputChange() {} };
  const renderer = new LiveMessageRenderer(new FakeDocument(), conversation, new LiveMessageParser(), { bind() {} });
  const entry = {
    article: new FakeElement("article"),
    output: new FakeElement("div"),
    body: new FakeElement("pre"),
    summaryText: new FakeElement("span"),
    toolName: "subagent",
  };
  let policyAtRender;
  renderer.renderSubagentPrompt = () => {};
  renderer.renderToolSummary = () => {};
  renderer.renderToolTranscriptBody = () => { policyAtRender = entry.output.dataset.toolOutputWraps; };
  const event = {
    type: "tool_execution_update",
    toolName: "subagent",
    partialResult: {
      content: [{ type: "text", text: "general progress" }],
      details: { status: "running", tools: [], usage: {} },
    },
  };

  renderer.updateLiveToolExecution(entry, event, false);

  assert.equal(entry.article.classList.contains("message--tool-transcript"), true);
  assert.equal(entry.output.dataset.toolOutputWraps, "false");
  assert.equal(policyAtRender, "false");
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
