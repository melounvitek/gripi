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
