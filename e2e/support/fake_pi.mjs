#!/usr/bin/env node

import { appendFileSync, existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { randomUUID } from "node:crypto";
import { StringDecoder } from "node:string_decoder";
import { prompts, replies, tool } from "./contract.mjs";

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
let sessionPersisted = false;
let entrySequence = 0;
const timers = new Set();

if (sessionPath) loadSession(sessionPath);
else prepareNewSession();
log({ event: "started", sessionPath, cwd: process.cwd() });

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
  schedule(120, () => completeWithTool(reply));
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
  emit({ type: "agent_settled" });
}

function acceptExtensionResponse(command) {
  if (command.id !== pendingExtensionRequest) return;
  pendingExtensionRequest = null;
  completeAssistant(command.confirmed ? replies.extensionApproved : "Release approval was declined.");
}

function completeWithTool(reply) {
  const toolCallId = `call_${randomUUID().slice(0, 8)}`;
  const toolMessage = assistantMessage([{ type: "toolCall", id: toolCallId, name: "bash", arguments: { command: tool.command } }], "toolUse");
  emit({ type: "message_start", message: { ...toolMessage, content: [] } });
  emit({ type: "message_update", message: toolMessage, assistantMessageEvent: { type: "toolcall_end", contentIndex: 0, toolCall: toolMessage.content[0], partial: toolMessage } });
  emit({ type: "message_end", message: toolMessage });
  appendMessage(toolMessage);
  emit({ type: "tool_execution_start", toolCallId, toolName: "bash", args: { command: tool.command } });
  emit({ type: "tool_execution_update", toolCallId, toolName: "bash", args: { command: tool.command }, partialResult: { content: [{ type: "text", text: tool.result }], details: {} } });
  const result = { content: [{ type: "text", text: tool.result }], details: {} };
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
  schedule(120, () => completeAssistant(reply));
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
  process.exit(0);
}
