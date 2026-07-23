import assert from "node:assert/strict";
import { test } from "node:test";

import { parseNativeBash } from "../public/assets/bash.js";
import { applyComposerPathCompletion, composerPathContext } from "../public/assets/composer_autocomplete_controller.js";
import { downloadResponse } from "../public/assets/downloads.js";
import { activateToolOutputRegion, deactivateToolOutputRegion } from "../public/assets/dom.js";
import {
  compactNumber,
  eventErrorText,
  eventTimestamp,
  extensionUiRequestNotice,
  formatWaitDuration,
  imageAttachmentLabel,
  messageFingerprint,
  messageRoleKey,
  messageRoleLabel,
  notificationReplyPreview,
  sessionExportSlashCommand,
  sessionNameFromEvent,
  sessionNameSlashCommand,
} from "../public/assets/formatting.js";
import { LiveMessageParser } from "../public/assets/live_message_parser.js";
import { selectedThinkingLevel, supportedThinkingLevels } from "../public/assets/model.js";
import { eventPollingDelay } from "../public/assets/polling.js";
import { keyboardScrollKey } from "../public/assets/shortcuts.js";
import { hasTerminalControls, renderTerminalOutput } from "../public/assets/terminal_output_renderer.js";
import { TREE_FILTERS, TREE_SUMMARY_CHOICES, TreeSessionModel } from "../public/assets/tree_session_controller.js";
import { newSessionModalUrl, sessionFragmentUrl, sessionUrl } from "../public/assets/urls.js";

test("formatting and message helpers preserve browser-facing semantics", () => {
  assert.equal(compactNumber(1500), "1.5k");
  assert.equal(formatWaitDuration(125000), "2m 05s");
  assert.equal(imageAttachmentLabel(1), "1 image attached");
  assert.equal(notificationReplyPreview("**Bold** and `code` [Label](https://example.com)"), "Bold and code Label");
  assert.match(messageFingerprint("toolResult", " Done\r\n", "123"), /^tool:123:[0-9a-f]+$/);
  assert.deepEqual([messageRoleKey("bashExecution"), messageRoleLabel("bashExecution")], ["tool", "shell"]);
  assert.equal(sessionNameSlashCommand("/name Useful name"), true);
  assert.equal(sessionNameSlashCommand("/rename Useful name"), false);
  assert.deepEqual(sessionExportSlashCommand(" /export Quarterly report "), { filename: "Quarterly report" });
  assert.deepEqual(sessionExportSlashCommand('/export "Quarterly report.html"'), { filename: "Quarterly report.html" });
  assert.deepEqual(sessionExportSlashCommand("/export"), { filename: "" });
  assert.equal(sessionExportSlashCommand("/export\nreport"), null);
  assert.equal(sessionNameFromEvent({ type: "session_info_changed", name: "Changed" }), "Changed");
  assert.equal(sessionNameFromEvent({ type: "custom_message", content: "Changed" }), null);
  assert.equal(eventTimestamp({ gatewayTimestamp: 1234, timestamp: "native" }), 1234);
});

test("extension notices and errors distinguish supported and terminal-only UI", () => {
  assert.equal(extensionUiRequestNotice({ type: "extension_ui_request", method: "select" }), null);
  assert.deepEqual(extensionUiRequestNotice({ type: "extension_ui_request", method: "notify", message: "Review", notifyType: "warning" }), { role: "status", text: "Warning: Review" });
  assert.equal(eventErrorText({ type: "extension_error", extensionPath: "command:sessions", event: "command", error: "Cannot read properties of undefined (reading 'action')" }), "This extension command requires terminal UI that Gripi does not support yet.");
  assert.equal(eventErrorText({ type: "compaction_end", result: null, errorMessage: "Compaction failed" }), "Compaction failed");
});

test("native bash, polling, shortcut, and URL helpers remain directly importable", () => {
  assert.deepEqual(parseNativeBash("!  printf one\nprintf two  "), { command: "printf one\nprintf two", excludeFromContext: false });
  assert.deepEqual(parseNativeBash("!! git status"), { command: "git status", excludeFromContext: true });
  assert.equal(parseNativeBash(" !pwd"), null);
  assert.deepEqual(["PageDown", " ", "Spacebar", "Enter"].map((key) => keyboardScrollKey({ key })), [true, true, true, false]);
  assert.deepEqual([
    eventPollingDelay(false, "running", 0),
    eventPollingDelay(true, "running", 0),
    eventPollingDelay(false, "done", 2),
    eventPollingDelay(false, "running", 0, true),
  ], [250, 10000, 2000, 2000]);

  const location = { href: "https://example.test/?project=demo&session=old", origin: "https://example.test", search: "?project=demo&session=old" };
  assert.equal(sessionUrl("new path", location), "/?session=new+path&project=demo");
  assert.equal(sessionFragmentUrl("/?session=next&project=demo", location).href, "https://example.test/session_fragment?session=next&project=demo");
  assert.equal(newSessionModalUrl(undefined, location).href, "https://example.test/new_session_modal?project=demo&session=old");
});

test("download responses use the server filename and release temporary browser URLs", async () => {
  const clicked = [];
  const revoked = [];
  const anchor = { click() { clicked.push([this.href, this.download]); }, remove() {} };
  const document = {
    body: { append(element) { assert.equal(element, anchor); } },
    createElement(tag) { assert.equal(tag, "a"); return anchor; },
  };
  const urls = {
    createObjectURL(blob) { assert.equal(blob, "export contents"); return "blob:export"; },
    revokeObjectURL(url) { revoked.push(url); },
  };
  const response = {
    blob: async () => "export contents",
    headers: { get: () => "attachment; filename*=utf-8''Quarterly%20report.html" },
  };

  const options = { document, urls };
  const filename = await downloadResponse(response, "session.html", options);
  response.headers.get = () => "attachment; filename=report.html";
  const unquotedFilename = await downloadResponse(response, "report", options);
  response.headers.get = () => 'attachment; filename="a\\\"b.html"';
  const escapedFilename = await downloadResponse(response, "session.html", options);
  let cancelled = false;
  const cancelledFilename = await downloadResponse({
    blob: async () => { cancelled = true; return "cancelled contents"; },
    headers: { get: () => "attachment; filename=cancelled.html" },
  }, "session.html", { ...options, cancelled: () => cancelled });
  await new Promise((resolve) => setTimeout(resolve, 0));

  assert.equal(filename, "Quarterly report.html");
  assert.equal(unquotedFilename, "report.html");
  assert.equal(escapedFilename, 'a"b.html');
  assert.equal(cancelledFilename, null);
  assert.deepEqual(clicked, [["blob:export", "Quarterly report.html"], ["blob:export", "report.html"], ["blob:export", 'a"b.html']]);
  assert.deepEqual(revoked, ["blob:export", "blob:export", "blob:export"]);
});

test("tool output keyboard helpers apply and remove accessible region state", () => {
  const attributes = {};
  let focusOptions;
  const body = {
    setAttribute(name, value) { attributes[name] = value; },
    removeAttribute(name) { delete attributes[name]; },
    focus(options) { focusOptions = options; },
  };
  activateToolOutputRegion(body, { focus: true });
  assert.equal(body.tabIndex, 0);
  assert.deepEqual(attributes, { role: "region", "aria-label": "Expanded tool output" });
  assert.deepEqual(focusOptions, { preventScroll: true });
  deactivateToolOutputRegion(body);
  assert.equal(body.tabIndex, -1);
  assert.deepEqual(attributes, {});
});

test("model thinking levels follow capabilities and provide supported fallbacks", () => {
  assert.deepEqual(supportedThinkingLevels({ reasoning: false, thinkingLevelMap: { off: null } }), ["off"]);
  assert.deepEqual(supportedThinkingLevels({ reasoning: true }), ["off", "minimal", "low", "medium", "high"]);
  assert.deepEqual(supportedThinkingLevels({ reasoning: true, thinkingLevelMap: { off: null, minimal: null, xhigh: "xhigh", max: null } }), ["low", "medium", "high", "xhigh"]);
  assert.deepEqual([
    selectedThinkingLevel({ reasoning: true }, "max"),
    selectedThinkingLevel({ reasoning: true, thinkingLevelMap: { minimal: null } }, "minimal"),
    selectedThinkingLevel({ reasoning: false }, "high"),
  ], ["high", "low", "off"]);
});

test("composer path completion uses caret-local Pi-style contexts", () => {
  const value = "first line\nopen @src/uti suffix";
  const caret = value.indexOf(" suffix");
  const context = composerPathContext(value, caret);
  assert.deepEqual(context, { mode: "fuzzy", query: "src/uti", token: "@src/uti", start: 16, quoted: false });
  assert.deepEqual(applyComposerPathCompletion(value, caret, context, { path: "src/user interface.js", directory: false }), {
    value: "first line\nopen @\"src/user interface.js\"  suffix",
    selectionStart: 41,
  });
  assert.equal(composerPathContext("email@example", 13), null);
  assert.equal(composerPathContext("look src/ma", 11, { natural: true }).mode, "path");
});

test("live parser preserves reasoning, final answers, images, and tool identity", () => {
  const parser = new LiveMessageParser("/home/alice");
  const segments = parser.contentSegments([
    { type: "thinking", thinking: "**Reasoning**\n\nInspect files" },
    { type: "text", text: "Commentary", textSignature: JSON.stringify({ v: 1, id: "one", phase: "commentary" }) },
    { type: "toolCall", id: "tool-1", name: "read", arguments: { path: "/home/alice/project/app.js", offset: 2, limit: 3 } },
    { type: "image", mimeType: "image/png", data: "aGVsbG8=" },
    { type: "text", text: "Final answer", textSignature: JSON.stringify({ v: 1, id: "two", phase: "final_answer" }) },
  ], { role: "assistant" });
  assert.equal(segments[0].thinking, true);
  assert.equal(segments[0].text, "Inspect files");
  assert.equal(segments[1].finalAssistantResponse, false);
  assert.equal(segments[2].toolCallId, "tool-1");
  assert.deepEqual(segments[2].summaryParts, { name: "read", path: "~/project/app.js", range: "2-4" });
  assert.equal(segments[2].images[0].src, "data:image/png;base64,aGVsbG8=");
  assert.equal(segments[3].finalAssistantResponse, true);
  assert.equal(parser.finalAssistantReplyText({ content: [
    { type: "text", text: "Working", textSignature: JSON.stringify({ v: 1, id: "one", phase: "commentary" }) },
    { type: "text", text: "Done" },
  ] }), "Done");
});

test("tree model searches, folds, reparents, and follows practical navigation", () => {
  assert.deepEqual(TREE_FILTERS.map(({ value }) => value), ["default", "no-tools", "user-only", "labeled-only", "all"]);
  assert.deepEqual(TREE_SUMMARY_CHOICES.map(({ value }) => value), ["none", "default", "custom"]);
  const model = new TreeSessionModel([
    { entryId: "root", parentId: null, role: "user", text: "Start release", current: false },
    { entryId: "left", parentId: "root", role: "assistant", text: "Inspect API", current: true },
    { entryId: "left-child", parentId: "left", role: "user", text: "Ship Linux build", label: "checkpoint" },
    { entryId: "right", parentId: "root", role: "assistant", text: "Inspect docs" },
  ]);
  assert.equal(model.selectedId, "left");
  assert.equal(model.move("left"), "left");
  assert.deepEqual(model.visibleEntries().map(({ entryId }) => entryId), ["root", "left", "right"]);
  assert.equal(model.move("left"), "root");
  assert.equal(model.move("right"), "left");
  model.select("root");
  assert.equal(model.move("left"), "root");
  assert.deepEqual(model.visibleEntries().map(({ entryId }) => entryId), ["root"]);
  assert.equal(model.move("right"), "root");
  model.select("left");
  assert.equal(model.move("right"), "left");
  assert.deepEqual(model.visibleEntries().map(({ entryId }) => entryId), ["root", "left", "left-child", "right"]);
  model.setSearch("linux checkpoint");
  assert.deepEqual(model.visibleEntries().map(({ entryId }) => entryId), ["left-child"]);
  const structure = model.visibleStructure();
  assert.deepEqual(structure.roots.map(({ entryId }) => entryId), ["left-child"]);
  assert.equal(model.selectedId, "left-child");
});

test("terminal renderer handles controls, screen replacement, styling, and safety", async () => {
  assert.deepEqual([
    hasTerminalControls("one\r\ntwo"),
    hasTerminalControls("10%\r20%"),
    hasTerminalControls("\x1b[31mred"),
  ], [false, true, true]);

  const progress = await renderTerminalOutput("Progress 10%\rProgress 90%");
  assert.deepEqual(progress.lines.map(({ text }) => text), ["Progress 90%"]);
  const cleared = await renderTerminalOutput("old one\nold two\x1b[2J\x1b[Hnew one\nnew two");
  assert.deepEqual(cleared.lines.map(({ text }) => text), ["new one", "new two"]);
  const styled = await renderTerminalOutput("界 \x1b[1;3;4;31;44mred\x1b[0m \x1b[38;2;1;2;3mtrue\x1b[0m");
  const red = styled.lines[0].runs.find(({ text }) => text === "red");
  assert.deepEqual(red.style.foreground, { mode: "palette", value: 1 });
  assert.equal(red.style.bold, true);
  const unsafe = await renderTerminalOutput("\x1b]0;title\x07\x1b]52;c;Y2xpcA==\x07\x1b]8;;javascript:alert(1)\x07Safe\x1b]8;;\x07");
  assert.equal(unsafe.lines[0].text, "Safe");
  assert.equal(unsafe.lines[0].runs[0].style.link, undefined);
});

test("terminal renderer bounds input and geometry while retaining the latest screen", async () => {
  const rendered = await renderTerminalOutput(`${"x".repeat(200)}\x1b[2J\x1b[Hfinal`, { maxInputChars: 40, maxColumns: 20, maxRows: 10 });
  assert.equal(rendered.truncated, true);
  assert.ok(rendered.columns <= 20);
  assert.ok(rendered.rows <= 10);
  assert.equal(rendered.lines.at(-1).text, "final");
});
