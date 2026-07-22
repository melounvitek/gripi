#!/usr/bin/env node

import { appendFileSync, existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { randomUUID } from "node:crypto";
import { StringDecoder } from "node:string_decoder";
import { nativeBash, prompts, replies, tool } from "./contract.mjs";

const LONG_BASH_COMMANDS = new Set([nativeBash.cancel.command, nativeBash.overlap.command, nativeBash.mobileCancel.command]);
const resumedPath = valueAfter("--session");
const sessionsRoot = process.env.GRIPI_E2E_SESSIONS_ROOT;
let sessionPath = resumedPath || null;
let header = null;
let entries = [];
let leafId = null;
let sessionName = null;
let model = fakeModels()[0];
let thinkingLevel = "medium";
let busy = false;
let activeScenario = null;
let pendingExtensionRequest = null;
let activeBash = null;
const treeLabels = new Map();
const deferredBashMessages = [];
let sessionPersisted = false;
let entrySequence = 0;
const timers = new Set();

if (sessionPath) loadSession(sessionPath);
else prepareNewSession();
log({ event: "started", sessionPath, cwd: process.cwd() });
process.once("exit", () => log({ event: "stopped" }));

attachJsonlReader(process.stdin, handleCommand);
process.on("SIGTERM", shutdown);
process.on("SIGINT", shutdown);
process.stdin.resume();

function valueAfter(flag) {
  const index = process.argv.indexOf(flag);
  return index === -1 ? null : process.argv[index + 1];
}

function fakeModels() {
  return [
    {
      id: "fixture-model",
      name: "Fixture Model",
      api: "openai-responses",
      provider: "e2e",
      baseUrl: "http://127.0.0.1",
      reasoning: true,
      input: ["text"],
      contextWindow: 128000,
      maxTokens: 8192,
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }
    },
    {
      id: "contract-model",
      name: "Contract Model",
      api: "openai-responses",
      provider: "e2e",
      baseUrl: "http://127.0.0.1",
      reasoning: true,
      input: ["text", "image"],
      contextWindow: 200000,
      maxTokens: 16384,
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }
    }
  ];
}

function loadSession(filePath) {
  const records = readFileSync(filePath, "utf8").split("\n").filter(Boolean).map((line) => JSON.parse(line));
  header = records.shift() || null;
  entries = records;
  sessionPersisted = existsSync(filePath);
  leafId = entries.at(-1)?.id || null;
  sessionName = [...entries].reverse().find((entry) => entry.type === "session_info")?.name || null;
  const latestModel = [...entries].reverse().find((entry) => entry.type === "model_change");
  const latestThinking = [...entries].reverse().find((entry) => entry.type === "thinking_level_change");
  model = fakeModels().find((candidate) => candidate.provider === latestModel?.provider && candidate.id === latestModel?.modelId) || model;
  thinkingLevel = latestThinking?.thinkingLevel || thinkingLevel;
  entrySequence = entries.length;
}

function attachJsonlReader(stream, onRecord) {
  const decoder = new StringDecoder("utf8");
  let buffer = "";
  stream.on("data", (chunk) => {
    buffer += decoder.write(chunk);
    while (true) {
      const newline = buffer.indexOf("\n");
      if (newline === -1) break;
      let line = buffer.slice(0, newline);
      buffer = buffer.slice(newline + 1);
      if (line.endsWith("\r")) line = line.slice(0, -1);
      if (line) parseRecord(line, onRecord);
    }
  });
  stream.on("end", () => {
    buffer += decoder.end();
    if (buffer.endsWith("\r")) buffer = buffer.slice(0, -1);
    if (buffer) parseRecord(buffer, onRecord);
  });
}

function parseRecord(line, onRecord) {
  try {
    onRecord(JSON.parse(line));
  } catch (error) {
    send({ type: "response", command: "parse", success: false, error: `Failed to parse command: ${error.message}` });
  }
}

function handleCommand(command) {
  log({ event: "command", command });
  switch (command.type) {
    case "get_state":
      respond(command, true, { data: state() });
      break;
    case "get_entries":
      respondWithEntries(command);
      break;
    case "get_messages":
      respond(command, true, { data: { messages: entries.filter((entry) => entry.type === "message").map((entry) => entry.message) } });
      break;
    case "get_session_stats":
      respond(command, true, { data: sessionStats() });
      break;
    case "get_available_models":
      respond(command, true, { data: { models: fakeModels() } });
      break;
    case "get_commands":
      respond(command, true, { data: { commands: [] } });
      break;
    case "set_model":
      setModel(command);
      break;
    case "set_thinking_level":
      thinkingLevel = command.level;
      appendEntry("thinking_level_change", { thinkingLevel });
      respond(command, true);
      break;
    case "cycle_thinking_level":
      thinkingLevel = thinkingLevel === "high" ? "off" : "high";
      appendEntry("thinking_level_change", { thinkingLevel });
      respond(command, true, { data: { level: thinkingLevel } });
      break;
    case "set_session_name":
      sessionName = command.name || null;
      appendEntry("session_info", { name: sessionName });
      respond(command, true);
      break;
    case "compact":
      appendEntry("compaction", { summary: command.customInstructions || "Fixture compaction" });
      respond(command, true);
      emit({ type: "compaction_end" });
      break;
    case "get_fork_messages":
      respond(command, true, { data: { messages: forkMessages() } });
      break;
    case "fork":
      branchSession(command, command.entryId);
      break;
    case "clone":
      branchSession(command, leafId);
      break;
    case "prompt":
      acceptPrompt(command);
      break;
    case "steer":
      acceptSteer(command);
      break;
    case "follow_up":
      acceptFollowUp(command);
      break;
    case "abort":
      acceptAbort(command);
      break;
    case "bash":
      acceptBash(command);
      break;
    case "abort_bash":
      acceptBashAbort(command);
      break;
    case "extension_ui_response":
      acceptExtensionResponse(command);
      break;
    default:
      respond(command, false, { error: `Unsupported fake Pi command: ${command.type}` });
  }
}

function state() {
  return {
    model,
    thinkingLevel,
    isStreaming: busy,
    isCompacting: false,
    steeringMode: "one-at-a-time",
    followUpMode: "one-at-a-time",
    sessionFile: sessionPath,
    sessionId: header?.id || null,
    ...(sessionName ? { sessionName } : {}),
    autoCompactionEnabled: true,
    messageCount: entries.filter((entry) => entry.type === "message").length,
    pendingMessageCount: 0
  };
}

function sessionStats() {
  const messages = entries.filter((entry) => entry.type === "message").map((entry) => entry.message);
  return {
    sessionFile: sessionPath,
    sessionId: header?.id || null,
    userMessages: messages.filter((message) => message.role === "user").length,
    assistantMessages: messages.filter((message) => message.role === "assistant").length,
    toolCalls: 1,
    toolResults: messages.filter((message) => message.role === "toolResult").length,
    totalMessages: messages.length,
    tokens: { input: 100, output: 50, cacheRead: 0, cacheWrite: 0, total: 150 },
    cost: 0,
    contextUsage: { tokens: 150, contextWindow: model.contextWindow, percent: 1 }
  };
}

function respondWithEntries(command) {
  if (!command.since) {
    respond(command, true, { data: { entries, leafId } });
    return;
  }
  const cursor = entries.findIndex((entry) => entry.id === command.since);
  if (cursor === -1) {
    respond(command, false, { error: `Entry not found: ${command.since}` });
    return;
  }
  respond(command, true, { data: { entries: entries.slice(cursor + 1), leafId } });
}

function setModel(command) {
  const selected = fakeModels().find((candidate) => candidate.provider === command.provider && candidate.id === command.modelId);
  if (!selected) {
    respond(command, false, { error: `Model not found: ${command.provider}/${command.modelId}` });
    return;
  }
  model = selected;
  appendEntry("model_change", { provider: model.provider, modelId: model.id });
  respond(command, true, { data: model });
}

function acceptPrompt(command) {
  if (acceptTreeBridge(command)) return;
  if (busy) {
    respond(command, false, { error: "Agent is already streaming" });
    return;
  }
  const user = userMessage(command.message);
  appendMessage(user);
  respond(command, true);
  busy = true;
  activeScenario = command.message;
  emit({ type: "agent_start" });
  emitMessage(user);
  emit({ type: "turn_start" });

  if (command.message === prompts.extension) {
    schedule(150, () => {
      pendingExtensionRequest = "e2e-release-approval";
      emit({
        type: "extension_ui_request",
        id: pendingExtensionRequest,
        method: "confirm",
        title: "Approve release?",
        message: "Allow the deterministic release?"
      });
    });
    return;
  }
  if ([prompts.steerStart, prompts.followUpStart, prompts.abortStart].includes(command.message)) return;

  const reply = path.basename(process.cwd()).startsWith("new-session-") ? replies.newSession : replies.standard;
  if (command.message === prompts.terminal) {
    schedule(120, () => completeWithTool(reply, { command: tool.terminalCommand, updates: tool.terminalUpdates, updateDelay: 350, completionDelay: 800 }));
  } else {
    schedule(120, () => completeWithTool(reply));
  }
}

function acceptTreeBridge(command) {
  const match = command.message?.match(/^\/(gripi_tree_(?:snapshot|navigate|label|leaf)) ([a-f0-9]+) ([A-Za-z0-9_-]+)$/i);
  if (!match) return false;
  respond(command, true);
  const [, name, requestId, encoded] = match;
  let payload;
  try {
    payload = JSON.parse(Buffer.from(encoded, "base64url").toString("utf8"));
  } catch (_error) {
    emitTreeBridge(name, requestId, { ok: false, error: "Invalid extension request payload" });
    return true;
  }
  if (name === "gripi_tree_snapshot") {
    const filter = payload.filter || "default";
    emitTreeBridge(name, requestId, {
      ok: true,
      entries: treeEntries(filter),
      leafId,
      truncated: false,
      totalEntries: entries.length,
      filter,
      settings: { treeFilterMode: "default", branchSummary: { skipPrompt: false } }
    });
  } else if (name === "gripi_tree_leaf") {
    emitTreeBridge(name, requestId, { ok: true, leafId });
  } else if (name === "gripi_tree_navigate") {
    const target = entries.find((entry) => entry.id === payload.entryId);
    if (!target) emitTreeBridge(name, requestId, { ok: false, error: `Tree entry not found: ${payload.entryId}` });
    else {
      leafId = target.id;
      emitTreeBridge(name, requestId, { ok: true, cancelled: false, ...(target.message?.role === "user" ? { editorText: textContent(target.message.content) } : {}) });
    }
  } else {
    const target = entries.find((entry) => entry.id === payload.entryId);
    if (!target) emitTreeBridge(name, requestId, { ok: false, error: `Tree entry not found: ${payload.entryId}` });
    else {
      if (payload.label) treeLabels.set(payload.entryId, payload.label);
      else treeLabels.delete(payload.entryId);
      emitTreeBridge(name, requestId, { ok: true, entryId: payload.entryId, label: payload.label || null });
    }
  }
  return true;
}

function emitTreeBridge(name, requestId, result) {
  emit({ type: "extension_ui_request", method: "setStatus", statusKey: `${name}:${requestId}`, statusText: JSON.stringify(result) });
}

function treeEntries(filter) {
  const projected = [];
  let visibleDepth = 0;
  let visibleParent = null;
  for (const entry of entries) {
    const role = entry.message?.role || entry.type;
    let visible;
    if (filter === "all") visible = true;
    else if (filter === "user-only") visible = entry.type === "message" && role === "user";
    else if (filter === "labeled-only") visible = treeLabels.has(entry.id);
    else if (filter === "no-tools") visible = entry.type !== "message" || role !== "toolResult";
    else visible = !["label", "custom", "model_change", "thinking_level_change", "session_info"].includes(entry.type);
    if (!visible) continue;
    projected.push({
      entryId: entry.id,
      parentId: visibleParent,
      depth: visibleDepth,
      type: entry.type,
      role,
      text: textContent(entry.message?.content || entry.summary || "").replace(/\s+/g, " ").trim().slice(0, 512),
      timestamp: entry.timestamp,
      current: entry.id === leafId,
      latest: entry === entries.at(-1),
      ...(role === "user" ? { messageKind: "user" } : {}),
      ...(treeLabels.has(entry.id) ? { label: treeLabels.get(entry.id) } : {})
    });
    visibleParent = entry.id;
    visibleDepth += 1;
  }
  return projected;
}

function textContent(content) {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content.filter((part) => typeof part === "string" || part?.type === "text").map((part) => typeof part === "string" ? part : part.text).join("");
}

function forkMessages() {
  return entries.filter((entry) => entry.type === "message" && entry.message?.role === "user").map((entry) => ({ entryId: entry.id, text: textContent(entry.message.content) }));
}

function branchSession(command, targetId) {
  const targetIndex = entries.findIndex((entry) => entry.id === targetId);
  const branchEntries = targetIndex < 0 ? [...entries] : entries.slice(0, targetIndex + 1);
  sessionPath = path.join(sessionsRoot, "e2e", `generated-${randomUUID()}.jsonl`);
  entries = branchEntries;
  leafId = entries.at(-1)?.id || null;
  mkdirSync(path.dirname(sessionPath), { recursive: true });
  writeFileSync(sessionPath, `${[header, ...entries].map((record) => JSON.stringify(record)).join("\n")}\n`);
  sessionPersisted = true;
  respond(command, true, { data: { text: targetId ? textContent(entries.at(-1)?.message?.content) : "" } });
}

function acceptSteer(command) {
  if (!busy || activeScenario !== prompts.steerStart) {
    respond(command, false, { error: "No steer scenario is active" });
    return;
  }
  respond(command, true);
  emit({ type: "queue_update", steering: [command.message], followUp: [] });
  schedule(350, () => {
    emit({ type: "queue_update", steering: [], followUp: [] });
    const user = userMessage(command.message);
    appendMessage(user);
    emitMessage(user);
    completeAssistant(replies.steer);
  });
}

function acceptFollowUp(command) {
  if (!busy || activeScenario !== prompts.followUpStart) {
    respond(command, false, { error: "No follow-up scenario is active" });
    return;
  }
  respond(command, true);
  emit({ type: "queue_update", steering: [], followUp: [command.message] });
  schedule(350, () => {
    emit({ type: "queue_update", steering: [], followUp: [] });
    const user = userMessage(command.message);
    appendMessage(user);
    emitMessage(user);
    completeAssistant(replies.followUp);
  });
}

function acceptAbort(command) {
  respond(command, true);
  if (!busy) return;
  clearTimers();
  busy = false;
  activeScenario = null;
  pendingExtensionRequest = null;
  emit({ type: "agent_end", messages: [], willRetry: false });
  persistDeferredBashMessages();
  emit({ type: "agent_settled" });
}

function acceptBash(command) {
  if (activeBash) {
    respond(command, false, { error: "A bash command is already running" });
    return;
  }

  const operation = { command, timer: null };
  activeBash = operation;
  const delay = LONG_BASH_COMMANDS.has(command.command) ? 30_000 : 120;
  operation.timer = setTimeout(() => completeBash(operation, bashResult(command.command)), delay);
}

function acceptBashAbort(command) {
  respond(command, true);
  if (!activeBash) return;

  completeBash(activeBash, { output: "", cancelled: true, truncated: false });
}

function completeBash(operation, result) {
  if (activeBash !== operation) return;
  clearTimeout(operation.timer);
  activeBash = null;
  const message = {
    role: "bashExecution",
    command: operation.command.command,
    output: result.output,
    ...(result.exitCode === undefined ? {} : { exitCode: result.exitCode }),
    cancelled: result.cancelled,
    truncated: result.truncated,
    timestamp: Date.now(),
    ...(operation.command.excludeFromContext === true ? { excludeFromContext: true } : {})
  };
  if (busy) deferredBashMessages.push(message);
  else appendMessage(message);
  respond(operation.command, true, { data: result });
}

function bashResult(command) {
  if (command === nativeBash.included.command) return completedBashResult(nativeBash.included.output);
  if (command === nativeBash.excluded.command) return completedBashResult(nativeBash.excluded.output);
  if (command === nativeBash.nonzero.command) return completedBashResult(nativeBash.nonzero.output, nativeBash.nonzero.exitCode);

  return completedBashResult(`Fake Pi completed: ${command}\n`);
}

function completedBashResult(output, exitCode = 0) {
  return { output, exitCode, cancelled: false, truncated: false };
}

function persistDeferredBashMessages() {
  while (deferredBashMessages.length > 0) appendMessage(deferredBashMessages.shift());
}

function acceptExtensionResponse(command) {
  if (command.id !== pendingExtensionRequest) return;
  pendingExtensionRequest = null;
  completeAssistant(command.confirmed ? replies.extensionApproved : "Release approval was declined.");
}

function completeWithTool(reply, options = {}) {
  const command = options.command || tool.command;
  const updates = options.updates || [tool.result];
  const toolCallId = `call_${randomUUID().slice(0, 8)}`;
  const toolMessage = assistantMessage([{ type: "toolCall", id: toolCallId, name: "bash", arguments: { command } }], "toolUse");
  emit({ type: "message_start", message: { ...toolMessage, content: [] } });
  emit({ type: "message_update", message: toolMessage, assistantMessageEvent: { type: "toolcall_end", contentIndex: 0, toolCall: toolMessage.content[0], partial: toolMessage } });
  emit({ type: "message_end", message: toolMessage });
  appendMessage(toolMessage);
  emit({ type: "tool_execution_start", toolCallId, toolName: "bash", args: { command } });

  const finish = () => {
    const result = { content: [{ type: "text", text: updates.at(-1) }], details: {} };
    emit({ type: "tool_execution_end", toolCallId, toolName: "bash", result, isError: false });
    const toolResult = {
      role: "toolResult",
      toolCallId,
      toolName: "bash",
      content: result.content,
      details: {},
      isError: false,
      timestamp: Date.now()
    };
    appendMessage(toolResult);
    emitMessage(toolResult);
    emit({ type: "turn_end", message: toolMessage, toolResults: [toolResult] });
    emit({ type: "turn_start" });
    schedule(options.completionDelay || 120, () => completeAssistant(reply));
  };
  const publishUpdate = (index) => {
    emit({ type: "tool_execution_update", toolCallId, toolName: "bash", args: { command }, partialResult: { content: [{ type: "text", text: updates[index] }], details: {} } });
    if (index === updates.length - 1) finish();
    else schedule(options.updateDelay || 0, () => publishUpdate(index + 1));
  };
  publishUpdate(0);
}

function completeAssistant(reply) {
  const timestamp = Date.now();
  const started = assistantMessage([], "stop", timestamp);
  const partial = assistantMessage([{ type: "text", text: reply.slice(0, Math.ceil(reply.length / 2)) }], "stop", timestamp);
  const completed = assistantMessage([{ type: "text", text: reply }], "stop", timestamp);
  emit({ type: "message_start", message: started });
  emit({ type: "message_update", message: partial, assistantMessageEvent: { type: "text_delta", contentIndex: 0, delta: partial.content[0].text, partial } });
  emit({ type: "message_update", message: completed, assistantMessageEvent: { type: "text_delta", contentIndex: 0, delta: reply.slice(partial.content[0].text.length), partial: completed } });
  emit({ type: "message_end", message: completed });
  appendMessage(completed);
  emit({ type: "turn_end", message: completed, toolResults: [] });
  emit({ type: "agent_end", messages: [completed], willRetry: false });
  busy = false;
  activeScenario = null;
  persistDeferredBashMessages();
  emit({ type: "agent_settled" });
}

function prepareNewSession() {
  if (!sessionsRoot) throw new Error("GRIPI_E2E_SESSIONS_ROOT is required for new fake sessions");
  sessionPath = path.join(sessionsRoot, "e2e", `generated-${randomUUID()}.jsonl`);
  header = {
    type: "session",
    version: 3,
    id: randomUUID(),
    timestamp: new Date().toISOString(),
    cwd: process.cwd()
  };
}

function appendMessage(value) {
  appendEntry("message", { message: value });
}

function appendEntry(type, fields) {
  if (!sessionPath) return;
  entrySequence += 1;
  const id = `${process.pid.toString(16).slice(-4).padStart(4, "0")}${entrySequence.toString(16).slice(-4).padStart(4, "0")}`;
  const entry = { type, id, parentId: leafId, timestamp: new Date().toISOString(), ...fields };
  entries.push(entry);
  leafId = id;
  if (sessionPersisted) {
    appendFileSync(sessionPath, `${JSON.stringify(entry)}\n`);
  } else if (type === "message" && fields.message?.role === "assistant") {
    mkdirSync(path.dirname(sessionPath), { recursive: true });
    writeFileSync(sessionPath, `${[header, ...entries].map((record) => JSON.stringify(record)).join("\n")}\n`);
    sessionPersisted = true;
  }
  return entry;
}

function userMessage(text) {
  return { role: "user", content: [{ type: "text", text }], timestamp: Date.now() };
}

function assistantMessage(content, stopReason, timestamp = Date.now()) {
  return {
    role: "assistant",
    content,
    api: model.api,
    provider: model.provider,
    model: model.id,
    usage: {
      input: 10,
      output: 5,
      cacheRead: 0,
      cacheWrite: 0,
      totalTokens: 15,
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 }
    },
    stopReason,
    timestamp
  };
}

function emitMessage(message) {
  emit({ type: "message_start", message });
  emit({ type: "message_end", message });
}

function respond(command, success, extra = {}) {
  send({
    ...(command.id === undefined ? {} : { id: command.id }),
    type: "response",
    command: command.type,
    success,
    ...extra
  });
}

function emit(event) {
  send({ ...event, timestamp: event.timestamp || new Date().toISOString() });
}

function send(record) {
  process.stdout.write(`${JSON.stringify(record)}\n`);
}

function schedule(delay, callback) {
  const timer = setTimeout(() => {
    timers.delete(timer);
    callback();
  }, delay);
  timers.add(timer);
}

function clearTimers() {
  for (const timer of timers) clearTimeout(timer);
  timers.clear();
}

function log(record) {
  const logPath = process.env.GRIPI_E2E_FAKE_PI_LOG;
  if (logPath) appendFileSync(logPath, `${JSON.stringify({ pid: process.pid, ...record })}\n`);
}

function shutdown() {
  clearTimers();
  if (activeBash) clearTimeout(activeBash.timer);
  process.exit(0);
}
