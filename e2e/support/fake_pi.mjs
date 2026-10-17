#!/usr/bin/env node

import { appendFileSync, existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { randomUUID } from "node:crypto";
import { StringDecoder } from "node:string_decoder";
import { activeRecovery, footerModel, footerStatus, live, mobileSubagents, nativeBash, paginatedSubagent, prompts, replies, subagents, tool, writeTool } from "./contract.mjs";

const LONG_BASH_COMMANDS = new Set([nativeBash.cancel.command, nativeBash.reload.command, nativeBash.overlap.command, nativeBash.mobileCancel.command]);
const resumedPath = valueAfter("--session");
const forkSource = valueAfter("--fork");
const sessionsRoot = process.env.GRIPI_E2E_SESSIONS_ROOT;
let sessionPath = resumedPath || null;
let header = null;
let entries = [];
let leafId = null;
let sessionName = null;
let model = fakeModels()[0];
let thinkingLevel = "medium";
let busy = false;
let compacting = false;
let activeScenario = null;
let queuedAbortSteer = null;
const pendingMessages = [];
let pendingExtensionRequest = null;
let extensionAnswers = [];
let activeBash = null;
let resourcesReloaded = false;
const treeLabels = new Map();
const deferredBashMessages = [];
let sessionPersisted = false;
let entrySequence = 0;
const timers = new Set();
let heldPromptEvents = null;
let recoveryMessage = null;
let liveGate = null;
let liveAbort = null;

if (sessionPath && existsSync(sessionPath)) loadSession(sessionPath);
else if (forkSource) forkSession(forkSource);
// Like Pi, a --session file that does not exist yet gets a new session, which Pi saves there.
else prepareNewSession(sessionPath);
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
  const models = [
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
  return [...models, { ...models[0], ...footerModel }];
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
    case "get_commands": {
      const commands = [
        { name: "immediate-command", description: "Execute immediately", source: "extension" },
        { name: "steer-template", description: "Steer from a prompt template", source: "prompt" }
      ];
      if (resourcesReloaded) commands.push({ name: "fresh-resource", description: "Loaded after reload", source: "prompt" });
      respond(command, true, { data: { commands } });
      break;
    }
    case "export_html":
      writeFileSync(command.outputPath, "<!doctype html><title>Fixture session export</title>", "utf8");
      respond(command, true, { data: { path: command.outputPath } });
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
    case "new_session":
      replaceWithNewSession(command);
      break;
    case "compact":
      if (busy) {
        busy = false;
        activeScenario = null;
        emit({ type: "agent_end", messages: [], willRetry: false });
        emit({ type: "agent_settled" });
      }
      compacting = true;
      emit({ type: "compaction_start" });
      schedule(4000, () => {
        const summary = command.customInstructions || "Fixture compaction";
        const firstKeptEntryId = leafId;
        appendEntry("compaction", { summary });
        compacting = false;
        emit({ type: "compaction_end", result: { summary, firstKeptEntryId, tokensBefore: 150 }, aborted: false, willRetry: false });
        respond(command, true);
      });
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
      acceptQueuedMessage(command, "steering", replies.steer);
      break;
    case "follow_up":
      acceptQueuedMessage(command, "followUp", replies.followUp);
      break;
    case "clear_queue": {
      const data = queuedMessages();
      clearPendingMessages();
      respond(command, true, { data });
      break;
    }
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
    isCompacting: compacting,
    steeringMode: "one-at-a-time",
    followUpMode: "one-at-a-time",
    sessionFile: sessionPath,
    sessionId: header?.id || null,
    ...(sessionName ? { sessionName } : {}),
    autoCompactionEnabled: true,
    messageCount: entries.filter((entry) => entry.type === "message").length,
    pendingMessageCount: pendingMessages.length
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
  if (acceptReloadBridge(command) || acceptTreeBridge(command)) return;
  if (command.message === "/immediate-command") {
    respond(command, true, { data: { disposition: "handled" } });
    return;
  }
  if (busy && command.streamingBehavior === "steer") {
    acceptQueuedMessage(command, "steering", replies.steer);
    return;
  }
  if (busy && command.streamingBehavior === "followUp") {
    acceptQueuedMessage(command, "followUp", replies.followUp);
    return;
  }
  if (busy) {
    respond(command, false, { error: "Agent is already streaming" });
    return;
  }
  const user = userMessage(command.message);
  appendMessage(user);
  respond(command, true, { data: { disposition: "started" } });
  busy = true;
  activeScenario = command.message;
  if (process.env.GRIPI_E2E_HOLD_PROMPT_EVENTS === "1") heldPromptEvents = [];
  emit({ type: "agent_start" });
  emitMessage(user);
  emit({ type: "turn_start" });

  if (command.message === activeRecovery.toolsPrompt) {
    schedule(120, startRecoveryTools);
    return;
  }
  for (const phase of ["thinking", "preparation"]) {
    for (const transport of ["cumulative", "delta", "streaming"]) {
      if (command.message === activeRecovery[`${phase}Prompt`][transport]) {
        schedule(120, () => startRecoveryAssistant(phase, transport));
        return;
      }
    }
  }
  if ([prompts.assistantError, prompts.assistantPartialError].includes(command.message)) {
    schedule(120, () => {
      const started = assistantMessage([], "stop");
      emit({ type: "message_start", message: started });
      const content = command.message === prompts.assistantPartialError ? [{ type: "text", text: "Partial answer before failure" }] : [];
      const partial = { ...started, content };
      emit({ type: "message_update", message: partial });
      schedule(content.length ? 1500 : 0, () => finishAssistant({ ...partial, stopReason: "error", errorMessage: "Provided authentication token is expired." }));
    });
    return;
  }
  if ([prompts.parallelSubagents, prompts.parallelSubagentsMobile].includes(command.message)) {
    schedule(120, startParallelSubagents);
    return;
  }
  if (command.message === prompts.paginatedSubagent) {
    schedule(120, startPaginatedSubagent);
    return;
  }
  if (command.message === "Hold a progress reply until final handoff") {
    schedule(150, () => {
      const progress = assistantMessage([{ type: "text", text: "Progress saved; still working before handoff." }], "stop");
      appendMessage(progress);
      emitMessage(progress);
      emit({ type: "turn_end", message: progress, toolResults: [] });
      emit({ type: "agent_end", messages: [progress], willRetry: false });
      // An extension can continue after agent_end; only agent_settled hands control back.
      pendingExtensionRequest = "e2e-settled-handoff";
      emit({ type: "extension_ui_request", id: pendingExtensionRequest, method: "confirm", title: "Finish the handoff?" });
    });
    return;
  }
  if (command.message === prompts.extension) {
    schedule(150, () => {
      pendingExtensionRequest = "e2e-release-approval";
      emit({ type: "extension_ui_request", method: "setStatus", ...footerStatus });
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
  if (command.message === prompts.extensionRace) {
    schedule(150, () => {
      pendingExtensionRequest = "e2e-extension-final";
      emit({ type: "extension_ui_request", id: "e2e-extension-expiring", method: "confirm", title: "Expiring request", timeout: 2000 });
      emit({ type: "extension_ui_request", id: "e2e-extension-retry", method: "confirm", title: "Retry request" });
      emit({ type: "extension_ui_request", id: pendingExtensionRequest, method: "confirm", title: "Final queued request" });
    });
    return;
  }
  if (command.message === prompts.extensionKinds) {
    schedule(150, () => {
      extensionAnswers = [];
      pendingExtensionRequest = "e2e-extension-editor";
      emit({ type: "extension_ui_request", id: "e2e-extension-select", method: "select", title: "Pick a target", options: ["staging", "production"] });
      emit({ type: "extension_ui_request", id: "e2e-extension-input", method: "input", title: "Release name", placeholder: "v1.0" });
      emit({ type: "extension_ui_request", id: pendingExtensionRequest, method: "editor", title: "Release notes", prefill: "Draft notes" });
    });
    return;
  }
  if ([prompts.steerStart, prompts.followUpStart, prompts.abortStart, prompts.clearQueueStart].includes(command.message)) return;
  if (command.message === prompts.deltaStreaming) {
    schedule(120, completeDeltaAssistant);
    return;
  }
  if (startLiveScenario(command.message)) return;

  let reply = replies.standard;
  if (path.basename(process.cwd()).startsWith("new-session-")) reply = replies.newSession;
  else if (command.message === prompts.markdownTable) reply = replies.markdownTable;
  else if (command.message === prompts.markdownRaw) reply = replies.markdownRaw;
  else if (command.message === prompts.markdownFence) reply = replies.markdownFence;
  else if (command.message === prompts.markdownHeadings) reply = replies.markdownHeadings;

  if (command.message === prompts.imageRead) {
    schedule(120, () => completeWithImageRead(reply));
  } else if (command.message === prompts.longCommand) {
    schedule(120, () => completeWithTool(reply, { command: tool.longCommand, initialCommand: "pi --no-session", initialCommandDelay: 750, resultDelay: 1500, completionDelay: 1200 }));
  } else if (command.message === prompts.terminal) {
    schedule(120, () => completeWithTool(reply, { command: tool.terminalCommand, updates: tool.terminalUpdates, updateDelay: 350, completionDelay: 800 }));
  } else if (command.message === prompts.wrappedToolOutput) {
    schedule(120, () => completeWithTool(reply, { command: tool.wrappedCommand, updates: [tool.wrappedOutput] }));
  } else if (command.message === prompts.writeOutput) {
    schedule(120, () => completeWithWrite(reply));
  } else {
    schedule(120, () => completeWithTool(reply));
  }
}

function acceptReloadBridge(command) {
  const match = command.message?.match(/^\/gripi_reload ([a-f0-9]+) ([A-Za-z0-9_-]+)$/i);
  if (!match) return false;
  respond(command, true);
  let result = { ok: true };
  if (compacting) result = { ok: false, error: "Wait for compaction to finish before reloading" };
  else if (busy) result = { ok: false, error: "Session is busy" };
  if (result.ok) resourcesReloaded = true;
  emit({ type: "extension_ui_request", method: "setStatus", statusKey: `gripi_reload:${match[1]}`, statusText: JSON.stringify(result) });
  return true;
}

function acceptTreeBridge(command) {
  const match = command.message?.match(/^\/(gripi_tree_(?:snapshot|navigate|label|leaf)|gripi_scoped_models) ([a-f0-9]+) ([A-Za-z0-9_-]+)$/i);
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
  if (["gripi_tree_navigate", "gripi_tree_label"].includes(name) && (busy || compacting)) {
    emitTreeBridge(name, requestId, { ok: false, error: "Session is busy" });
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
  } else if (name === "gripi_scoped_models") {
    // controls-project stands for Pi without enabledModels, where nothing is scoped.
    const scoped = path.basename(header?.cwd || "") === "controls-project" ? [] : fakeModels().slice(0, 2);
    emitTreeBridge(name, requestId, { ok: true, models: scoped.map(({ provider, id }) => ({ provider, id })) });
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

function queuedMessages() {
  return {
    steering: pendingMessages.filter((pending) => pending.kind === "steering").map((pending) => pending.message),
    followUp: pendingMessages.filter((pending) => pending.kind === "followUp").map((pending) => pending.message)
  };
}

function clearPendingMessages() {
  for (const pending of pendingMessages) {
    clearTimeout(pending.timer);
    timers.delete(pending.timer);
  }
  pendingMessages.length = 0;
  queuedAbortSteer = null;
  emit({ type: "queue_update", ...queuedMessages() });
}

function acceptQueuedMessage(command, kind, reply) {
  if (!busy) {
    respond(command, false, { error: "No active run to queue a message for" });
    return;
  }
  const pending = { kind, message: command.message, timer: null };
  pendingMessages.push(pending);
  respond(command, true, { data: { disposition: "queued" } });
  emit({ type: "queue_update", ...queuedMessages() });
  if (kind === "steering" && command.message === prompts.queuedAbortSteer) {
    queuedAbortSteer = command.message;
    return;
  }
  // Keep browser queues stable through confirmation, reload, and second-tab checks.
  const delay = activeScenario === prompts.clearQueueStart ? 30_000 : command.message === "Restore this queued message" ? 10_000 : 350;
  pending.timer = schedule(delay, () => {
    pendingMessages.splice(pendingMessages.indexOf(pending), 1);
    emit({ type: "queue_update", ...queuedMessages() });
    const user = userMessage(command.message);
    appendMessage(user);
    emitMessage(user);
    completeAssistant(reply);
  });
}

function acceptAbort(command) {
  // Let tests hold lifecycle delivery without changing native prompt acceptance or state.
  const held = heldPromptEvents;
  heldPromptEvents = null;
  for (const event of held || []) send(event);
  if (!busy) {
    respond(command, true);
    return;
  }
  clearTimers();
  if (liveAbort) {
    const abortLive = liveAbort;
    liveAbort = null;
    abortLive();
    respond(command, true);
    return;
  }
  const abortSteer = queuedAbortSteer;
  if (pendingMessages.length) clearPendingMessages();
  if (abortSteer) {
    const user = userMessage(abortSteer);
    appendMessage(user);
    emitMessage(user);
    return;
  }
  if (recoveryMessage) {
    if (activeScenario === activeRecovery.toolsPrompt) {
      const toolResults = activeRecovery.tools.map((call) => {
        const result = { content: [{ type: "text", text: `${call.name} recovery aborted` }] };
        emit({ type: "tool_execution_end", toolCallId: call.id, toolName: call.name, result, isError: true });
        const message = { role: "toolResult", toolCallId: call.id, toolName: call.name, ...result, isError: true, timestamp: Date.now() };
        appendMessage(message);
        emitMessage(message);
        return message;
      });
      emit({ type: "turn_end", message: recoveryMessage, toolResults });
      emit({ type: "turn_start" });
      completeAssistant(replies.aborted, toolResults);
    } else {
      finishAssistant({ ...recoveryMessage, stopReason: "aborted" });
    }
    recoveryMessage = null;
    respond(command, true);
    return;
  }
  if ([prompts.parallelSubagents, prompts.parallelSubagentsMobile].includes(activeScenario)) {
    abortParallelSubagents();
    respond(command, true);
    return;
  }
  if (activeScenario === prompts.paginatedSubagent) {
    abortPaginatedSubagent();
    respond(command, true);
    return;
  }
  busy = false;
  activeScenario = null;
  pendingExtensionRequest = null;
  emit({ type: "agent_end", messages: [], willRetry: false });
  persistDeferredBashMessages();
  emit({ type: "agent_settled" });
  respond(command, true);
}

function acceptBash(command) {
  if (activeBash) {
    respond(command, false, { error: "A bash command is already running" });
    return;
  }

  if (command.command === nativeBash.failure.command) {
    respond(command, false, { error: nativeBash.failure.error });
    return;
  }

  const operation = { command, timer: null, output: "" };
  activeBash = operation;
  if (command.command === nativeBash.streaming.command) {
    // Streams its output while it runs, which ends only when aborted.
    const [first, second] = nativeBash.streaming.chunks;
    sendBashOutput(operation, first);
    operation.timer = setTimeout(() => sendBashOutput(operation, second), 1500);
    return;
  }
  if (LONG_BASH_COMMANDS.has(command.command)) {
    operation.timer = setTimeout(() => completeBash(operation, bashResult(command.command)), 30_000);
    return;
  }
  const result = bashResult(command.command);
  // Like Pi, stream the output under the command's ID before the response.
  send({ type: "bash_execution_update", id: command.id, delta: result.output });
  operation.timer = setTimeout(() => completeBash(operation, result), 120);
}

function acceptBashAbort(command) {
  respond(command, true);
  if (!activeBash) return;

  // Like Pi, an aborted command keeps the output it printed.
  completeBash(activeBash, { output: activeBash.output, cancelled: true, truncated: false });
}

function sendBashOutput(operation, delta) {
  operation.output += delta;
  send({ type: "bash_execution_update", id: operation.command.id, delta });
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
    ...(result.fullOutputPath ? { fullOutputPath: result.fullOutputPath } : {}),
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
  if (command === nativeBash.truncated.command) {
    return { ...completedBashResult(nativeBash.truncated.output), truncated: true, fullOutputPath: path.join(os.homedir(), nativeBash.truncated.fullOutputFile) };
  }
  // Shows what the gateway put in this process's environment.
  const variable = command.match(/^printenv (\w+)$/)?.[1];
  if (variable) return completedBashResult(`${process.env[variable] ?? ""}\n`);

  return completedBashResult(`Fake Pi completed: ${command}\n`);
}

function completedBashResult(output, exitCode = 0) {
  return { output, exitCode, cancelled: false, truncated: false };
}

function persistDeferredBashMessages() {
  while (deferredBashMessages.length > 0) appendMessage(deferredBashMessages.shift());
}

function acceptExtensionResponse(command) {
  if (liveGate && command.id === liveGate.id) {
    const { next } = liveGate;
    liveGate = null;
    next();
    return;
  }
  if (activeScenario === prompts.extensionKinds) extensionAnswers.push(command.value);
  if (command.id !== pendingExtensionRequest) return;
  pendingExtensionRequest = null;
  if (activeScenario === prompts.extensionKinds) {
    completeAssistant(`Extension answers: ${extensionAnswers.join(" / ")}`);
    return;
  }
  if (command.id === "e2e-settled-handoff") {
    emit({ type: "agent_start" });
    emit({ type: "turn_start" });
  }
  if (activeScenario === prompts.extensionRace) {
    completeAssistant(replies.extensionRaceComplete);
    return;
  }
  completeAssistant(command.confirmed ? replies.extensionApproved : "Release approval was declined.");
}

function startRecoveryTools() {
  recoveryMessage = assistantMessage(activeRecovery.tools.map((call) => ({ type: "toolCall", ...call })), "toolUse");
  emit({ type: "message_start", message: { ...recoveryMessage, content: [] } });
  emit({ type: "message_update", message: recoveryMessage, assistantMessageEvent: { type: "toolcall_end", contentIndex: 3, toolCall: recoveryMessage.content[3], partial: recoveryMessage } });
  emit({ type: "message_end", message: recoveryMessage });
  appendMessage(recoveryMessage);
  for (const call of activeRecovery.tools) {
    emit({ type: "tool_execution_start", toolCallId: call.id, toolName: call.name, args: call.arguments });
    if (call.name === "bash") emit({ type: "tool_execution_update", toolCallId: call.id, toolName: call.name, args: call.arguments, partialResult: { content: [{ type: "text", text: "bash recovery still running" }] } });
  }
}

function startRecoveryAssistant(phase, transport) {
  recoveryMessage = assistantMessage([], "stop");
  emit({ type: "message_start", message: recoveryMessage });
  const update = (event) => emit({
    type: "message_update",
    ...(transport === "cumulative" ? { message: recoveryMessage } : {}),
    assistantMessageEvent: { ...event, ...(transport === "cumulative" ? { partial: recoveryMessage } : {}) }
  });
  update({ type: "thinking_start", contentIndex: 0 });
  recoveryMessage.content.push({ type: "thinking", thinking: activeRecovery.previousThinking });
  update({ type: "thinking_delta", contentIndex: 0, delta: activeRecovery.previousThinking });
  update({ type: "thinking_end", contentIndex: 0, content: activeRecovery.previousThinking });
  update({ type: "text_start", contentIndex: 1 });
  recoveryMessage.content.push({ type: "text", text: activeRecovery.text });
  update({ type: "text_delta", contentIndex: 1, delta: activeRecovery.text });
  update({ type: "text_end", contentIndex: 1, content: activeRecovery.text });
  if (phase === "thinking") {
    const thinking = `${activeRecovery.thinking}\n\n${activeRecovery.unfinishedThinking}`;
    update({ type: "thinking_start", contentIndex: 2 });
    recoveryMessage.content.push({ type: "thinking", thinking });
    update({ type: "thinking_delta", contentIndex: 2, delta: thinking });
  } else if (transport === "streaming") {
    // Like current Pi, which names the call when it starts and then streams its arguments.
    const call = activeRecovery.streamingTool;
    update({ type: "toolcall_start", contentIndex: 2, id: call.id, toolName: "write" });
    update({ type: "toolcall_delta", contentIndex: 2, delta: `{"path": "${call.path}", "content": "${call.line}\\nUnfinished` });
  } else {
    update({ type: "toolcall_start", contentIndex: 2 });
    update({ type: "toolcall_delta", contentIndex: 2, delta: '{"path":"recovery' });
  }
}

function startParallelSubagents() {
  const scenario = activeScenario === prompts.parallelSubagentsMobile ? mobileSubagents : subagents;
  const toolMessage = assistantMessage([
    { type: "toolCall", id: scenario.firstCallId, name: "subagent", arguments: { task: scenario.firstPrompt } },
    { type: "toolCall", id: scenario.secondCallId, name: "subagent", arguments: { task: scenario.secondPrompt } }
  ], "toolUse");
  emit({ type: "message_start", message: { ...toolMessage, content: [] } });
  emit({ type: "message_update", message: toolMessage, assistantMessageEvent: { type: "toolcall_end", contentIndex: 1, toolCall: toolMessage.content[1], partial: toolMessage } });
  emit({ type: "message_end", message: toolMessage });
  appendMessage(toolMessage);

  emit({ type: "tool_execution_start", toolCallId: scenario.firstCallId, toolName: "subagent", args: { task: scenario.firstPrompt } });
  emit({ type: "tool_execution_start", toolCallId: scenario.secondCallId, toolName: "subagent", args: { task: scenario.secondPrompt } });
  emit({ type: "tool_execution_update", toolCallId: scenario.secondCallId, toolName: "subagent", partialResult: subagentExecutionResult("running", scenario.secondProgress) });
  schedule(150, () => {
    emit({ type: "tool_execution_end", toolCallId: scenario.firstCallId, toolName: "subagent", result: subagentExecutionResult("done", scenario.firstResult), isError: false });
  });
}

function abortParallelSubagents() {
  const scenario = activeScenario === prompts.parallelSubagentsMobile ? mobileSubagents : subagents;
  const firstResult = subagentExecutionResult("done", scenario.firstResult);
  const secondResult = subagentExecutionResult("error", scenario.secondResult);
  emit({ type: "tool_execution_end", toolCallId: scenario.secondCallId, toolName: "subagent", result: secondResult, isError: true });

  const toolResults = [
    { role: "toolResult", toolCallId: scenario.firstCallId, toolName: "subagent", content: firstResult.content, details: firstResult.details, isError: false, timestamp: Date.now() },
    { role: "toolResult", toolCallId: scenario.secondCallId, toolName: "subagent", content: secondResult.content, details: secondResult.details, isError: true, timestamp: Date.now() }
  ];
  toolResults.forEach((result) => {
    appendMessage(result);
    emitMessage(result);
  });
  emit({ type: "turn_end", toolResults });
  busy = false;
  activeScenario = null;
  emit({ type: "agent_end", messages: toolResults, willRetry: false });
  persistDeferredBashMessages();
  emit({ type: "agent_settled" });
}

function startPaginatedSubagent() {
  const toolMessage = assistantMessage([
    { type: "toolCall", id: paginatedSubagent.unrelatedCallId, name: "subagent", arguments: { task: "Keep unrelated work" } }
  ], "toolUse");
  emit({ type: "message_start", message: { ...toolMessage, content: [] } });
  emit({ type: "message_update", message: toolMessage, assistantMessageEvent: { type: "toolcall_end", contentIndex: 0, toolCall: toolMessage.content[0], partial: toolMessage } });
  emit({ type: "message_end", message: toolMessage });
  appendMessage(toolMessage);
  emit({ type: "tool_execution_start", toolCallId: paginatedSubagent.matchingCallId, toolName: "subagent", args: { task: "Persisted paginated task" } });
  emit({ type: "tool_execution_update", toolCallId: paginatedSubagent.matchingCallId, toolName: "subagent", partialResult: subagentExecutionResult("running", paginatedSubagent.liveProgress) });
  emit({ type: "tool_execution_start", toolCallId: paginatedSubagent.unrelatedCallId, toolName: "subagent", args: { task: "Keep unrelated work" } });
  schedule(1200, () => {
    emit({ type: "tool_execution_end", toolCallId: paginatedSubagent.matchingCallId, toolName: "subagent", result: subagentExecutionResult("done", "Paginated replay completed"), isError: false });
  });
}

function abortPaginatedSubagent() {
  emit({ type: "tool_execution_end", toolCallId: paginatedSubagent.unrelatedCallId, toolName: "subagent", result: subagentExecutionResult("error", "Paginated unrelated work aborted"), isError: true });
  emit({ type: "turn_end", toolResults: [] });
  busy = false;
  activeScenario = null;
  emit({ type: "agent_end", messages: [], willRetry: false });
  emit({ type: "agent_settled" });
}

function subagentExecutionResult(status, text) {
  return {
    content: [{ type: "text", text }],
    details: {
      status,
      tools: [],
      textItems: [text],
      usage: { turns: 1, input: 10, output: 5, contextTokens: 15 },
      model: "fixture-model"
    }
  };
}

function completeWithTool(reply, options = {}) {
  const command = options.command || tool.command;
  const updates = options.updates || [tool.result];
  const toolCallId = `call_${randomUUID().slice(0, 8)}`;
  const toolMessage = assistantMessage([{ type: "toolCall", id: toolCallId, name: "bash", arguments: { command } }], "toolUse");
  emit({ type: "message_start", message: { ...toolMessage, content: [] } });

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
    if (index === updates.length - 1 && options.resultDelay) schedule(options.resultDelay, finish);
    else if (index === updates.length - 1) finish();
    else schedule(options.updateDelay || 0, () => publishUpdate(index + 1));
  };
  const publishToolCall = () => {
    emit({ type: "message_update", message: toolMessage, assistantMessageEvent: { type: "toolcall_end", contentIndex: 0, toolCall: toolMessage.content[0], partial: toolMessage } });
    emit({ type: "message_end", message: toolMessage });
    appendMessage(toolMessage);
    emit({ type: "tool_execution_start", toolCallId, toolName: "bash", args: { command } });
    publishUpdate(0);
  };

  if (options.initialCommand) {
    const initialMessage = assistantMessage([{ type: "toolCall", id: toolCallId, name: "bash", arguments: { command: options.initialCommand } }], "toolUse");
    emit({ type: "message_update", message: initialMessage, assistantMessageEvent: { type: "toolcall_delta", contentIndex: 0, toolCall: initialMessage.content[0], partial: initialMessage } });
    schedule(options.initialCommandDelay || 50, publishToolCall);
  } else {
    publishToolCall();
  }
}

function completeWithWrite(reply) {
  const toolCallId = `call_${randomUUID().slice(0, 8)}`;
  const toolCall = { type: "toolCall", id: toolCallId, name: "write", arguments: { path: writeTool.path, content: writeTool.content } };
  const toolMessage = assistantMessage([toolCall], "toolUse");
  emit({ type: "message_start", message: { ...toolMessage, content: [] } });
  emit({ type: "message_update", message: toolMessage, assistantMessageEvent: { type: "toolcall_end", contentIndex: 0, toolCall, partial: toolMessage } });
  emit({ type: "message_end", message: toolMessage });
  appendMessage(toolMessage);
  emit({ type: "tool_execution_start", toolCallId, toolName: "write", args: toolCall.arguments });

  const result = { content: [{ type: "text", text: writeTool.result }], details: {} };
  emit({ type: "tool_execution_end", toolCallId, toolName: "write", result, isError: false });
  const toolResult = {
    role: "toolResult",
    toolCallId,
    toolName: "write",
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

function completeWithImageRead(reply) {
  const toolCallId = `call_${randomUUID().slice(0, 8)}`;
  const toolCall = { type: "toolCall", id: toolCallId, name: "read", arguments: { path: "/tmp/e2e-large-image.png" } };
  const toolMessage = assistantMessage([toolCall], "toolUse");
  emit({ type: "message_start", message: { ...toolMessage, content: [] } });
  emit({ type: "message_update", message: toolMessage, assistantMessageEvent: { type: "toolcall_end", contentIndex: 0, toolCall, partial: toolMessage } });
  emit({ type: "message_end", message: toolMessage });
  appendMessage(toolMessage);
  emit({ type: "tool_execution_start", toolCallId, toolName: "read", args: toolCall.arguments });

  const result = {
    content: [
      { type: "text", text: "Read image file [image/png]" },
      {
        type: "image",
        // A decodable PNG with padding keeps this result above the large-event threshold.
        data: Buffer.concat([
          Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a7XkAAAAASUVORK5CYII=", "base64"),
          Buffer.alloc(3 << 20)
        ]).toString("base64"),
        mimeType: "image/png"
      }
    ],
    details: {}
  };
  emit({ type: "tool_execution_end", toolCallId, toolName: "read", result, isError: false });
  const toolResult = {
    role: "toolResult",
    toolCallId,
    toolName: "read",
    content: result.content,
    details: {},
    isError: false,
    timestamp: Date.now()
  };
  appendMessage(toolResult);
  emitMessage(toolResult);
  emit({ type: "turn_end", message: toolMessage, toolResults: [toolResult] });
  emit({ type: "turn_start" });
  schedule(120, () => completeAssistant(reply, [toolMessage, toolResult]));
}

function completeAssistant(reply, priorMessages = []) {
  const timestamp = Date.now();
  const started = assistantMessage([], "stop", timestamp);
  const partial = assistantMessage([{ type: "text", text: reply.slice(0, Math.ceil(reply.length / 2)) }], "stop", timestamp);
  const completed = assistantMessage([{ type: "text", text: reply }], "stop", timestamp);
  emit({ type: "message_start", message: started });
  emit({ type: "message_update", message: partial, assistantMessageEvent: { type: "text_delta", contentIndex: 0, delta: partial.content[0].text, partial } });
  emit({ type: "message_update", message: completed, assistantMessageEvent: { type: "text_delta", contentIndex: 0, delta: reply.slice(partial.content[0].text.length), partial: completed } });
  finishAssistant(completed, priorMessages);
}

function completeDeltaAssistant() {
  const timestamp = Date.now();
  const started = assistantMessage([], "stop", timestamp);
  const thinking = `${replies.deltaThinking}\n\n${replies.deltaThinkingMore}`;
  const unfinishedText = `${replies.deltaTextStart}\n\nAnswer comp`;
  const completed = assistantMessage([
    { type: "thinking", thinking },
    { type: "text", text: replies.deltaText }
  ], "stop", timestamp);
  emit({ type: "message_start", message: started });
  emit({ type: "message_update", assistantMessageEvent: { type: "thinking_start", contentIndex: 0 } });
  emit({ type: "message_update", assistantMessageEvent: { type: "thinking_delta", contentIndex: 0, delta: replies.deltaThinking } });
  schedule(1000, () => {
    emit({ type: "message_update", assistantMessageEvent: { type: "thinking_delta", contentIndex: 0, delta: thinking.slice(replies.deltaThinking.length) } });
    schedule(1000, () => {
      emit({ type: "message_update", assistantMessageEvent: { type: "text_start", contentIndex: 1 } });
      emit({ type: "message_update", assistantMessageEvent: { type: "text_delta", contentIndex: 1, delta: unfinishedText } });
      // Long enough for the quote test to select the reply before it finishes.
      schedule(2000, () => {
        emit({ type: "message_update", assistantMessageEvent: { type: "thinking_end", contentIndex: 0, content: thinking } });
        emit({ type: "message_update", assistantMessageEvent: { type: "text_delta", contentIndex: 1, delta: replies.deltaText.slice(unfinishedText.length) } });
        emit({ type: "message_update", assistantMessageEvent: { type: "text_end", contentIndex: 1, content: replies.deltaText } });
        finishAssistant(completed);
      });
    });
  });
}

function finishAssistant(completed, priorMessages = []) {
  emit({ type: "message_end", message: completed });
  appendMessage(completed);
  emit({ type: "turn_end", message: completed, toolResults: [] });
  emit({ type: "agent_end", messages: [...priorMessages, completed], willRetry: false });
  busy = false;
  activeScenario = null;
  persistDeferredBashMessages();
  emit({ type: "agent_settled" });
}

function startLiveScenario(message) {
  const start = {
    [live.toolResults.prompt]: startLiveToolResults,
    [live.errors.transientRetry.prompt]: startTransientRetry,
    [live.errors.exhaustedRetry.prompt]: startExhaustedRetry,
    [live.errors.extensionErrors.prompt]: startExtensionErrors,
    [live.errors.summarizationRetry.prompt]: startSummarizationRetry,
    [live.customMessages.prompt]: startLiveCustomMessages,
    [live.extensionUi.prompt]: startLiveExtensionUi,
    [live.extensionUi.restorePrompt]: startRestoredExtensionUi,
    [live.footer.prompt]: startLiveFooter,
    [live.compaction.failurePrompt]: startFailedCompaction,
    [live.compaction.abortPrompt]: startAbortedCompaction,
    [live.compaction.duplicatePrompt]: startDuplicateCompaction,
    [live.thinking.prompt]: startFinishedThinking,
    [live.thinking.errorPrompt]: startFailedThinking,
    [live.thinking.abortPrompt]: startAbortedThinking
  }[message];
  if (!start) return false;
  schedule(120, start);
  return true;
}

// Pauses a characterization scenario at a confirmation, which also pauses the browser's event polling.
// The timeout only closes the dialog after a failed test, so it can't block the tests that follow.
function holdLiveScenario(title, next) {
  liveGate = { id: `e2e-live-gate-${randomUUID()}`, next };
  emit({ type: "extension_ui_request", id: liveGate.id, method: "confirm", title, timeout: 15_000 });
}

function startLiveToolResults() {
  const scenario = live.toolResults;
  const tools = [
    { tool: scenario.readOk, name: "read", args: { path: scenario.readOk.path }, isError: false, details: {} },
    { tool: scenario.readError, name: "read", args: { path: scenario.readError.path }, isError: true, details: {} },
    { tool: scenario.editOk, name: "edit", args: { path: scenario.editOk.path, edits: [{ oldText: scenario.editOk.oldText, newText: scenario.editOk.newText }] }, isError: false, details: { diff: scenario.editOk.diff, firstChangedLine: 2 } },
    { tool: scenario.editError, name: "edit", args: { path: scenario.editError.path, edits: [{ oldText: scenario.editError.oldText, newText: scenario.editError.newText }] }, isError: true, details: {} },
    { tool: scenario.writeOk, name: "write", args: { path: scenario.writeOk.path, content: scenario.writeOk.content }, isError: false, details: {} },
    { tool: scenario.writeError, name: "write", args: { path: scenario.writeError.path, content: scenario.writeError.content }, isError: true, details: {} }
  ];
  const calls = tools.map(({ tool, name, args }) => ({ type: "toolCall", id: tool.id, name, arguments: args }));
  const toolMessage = assistantMessage(calls, "toolUse");
  emit({ type: "message_start", message: { ...toolMessage, content: [] } });
  emit({ type: "message_update", message: toolMessage, assistantMessageEvent: { type: "toolcall_end", contentIndex: calls.length - 1, toolCall: calls.at(-1), partial: toolMessage } });
  emit({ type: "message_end", message: toolMessage });
  appendMessage(toolMessage);
  for (const call of calls) emit({ type: "tool_execution_start", toolCallId: call.id, toolName: call.name, args: call.arguments });

  holdLiveScenario(scenario.gates.results, () => {
    const toolResults = tools.map(({ tool, name, isError, details }) => {
      const result = { content: [{ type: "text", text: tool.result }], details };
      emit({ type: "tool_execution_end", toolCallId: tool.id, toolName: name, result, isError });
      const message = { role: "toolResult", toolCallId: tool.id, toolName: name, ...result, isError, timestamp: Date.now() };
      appendMessage(message);
      emitMessage(message);
      return message;
    });
    emit({ type: "turn_end", message: toolMessage, toolResults });
    emit({ type: "turn_start" });
    holdLiveScenario(scenario.gates.finish, () => completeAssistant(scenario.reply, [toolMessage, ...toolResults]));
  });
}

// Like Pi's auto-retry: the failed attempt ends its run, and the retry starts a new one before the run settles.
function startTransientRetry() {
  const scenario = live.errors.transientRetry;
  const failed = failAssistant(scenario.error);
  emit({ type: "agent_end", messages: [failed], willRetry: true });
  emit({ type: "auto_retry_start", attempt: 1, maxAttempts: 3, delayMs: 2000, errorMessage: scenario.error });
  holdLiveScenario(scenario.gate, () => {
    emit({ type: "agent_start" });
    emit({ type: "turn_start" });
    const reply = emitAssistantReply(scenario.reply);
    emit({ type: "auto_retry_end", success: true, attempt: 1 });
    emit({ type: "turn_end", message: reply, toolResults: [] });
    emit({ type: "agent_end", messages: [reply], willRetry: false });
    settleRun();
  });
}

function startExhaustedRetry() {
  const scenario = live.errors.exhaustedRetry;
  const failed = failAssistant(scenario.error);
  emit({ type: "agent_end", messages: [failed], willRetry: false });
  emit({ type: "auto_retry_end", success: false, attempt: 3, finalError: scenario.error });
  settleRun();
}

function failAssistant(errorMessage) {
  const failed = { ...assistantMessage([], "error"), errorMessage };
  emit({ type: "message_start", message: assistantMessage([], "stop", failed.timestamp) });
  emit({ type: "message_end", message: failed });
  appendMessage(failed);
  emit({ type: "turn_end", message: failed, toolResults: [] });
  return failed;
}

function startExtensionErrors() {
  const scenario = live.errors.extensionErrors;
  emit({ type: "extension_error", extensionPath: path.join(os.homedir(), ".pi", "agent", "extensions", "e2e-guard.ts"), event: "tool_call", error: scenario.error });
  // Pi's /sessions extension command fails like this without a terminal UI.
  emit({ type: "extension_error", extensionPath: "command:sessions", event: "command", error: "Cannot read properties of undefined (reading 'action')" });
  completeAssistant(scenario.reply);
}

function startSummarizationRetry() {
  const scenario = live.errors.summarizationRetry;
  replyBeforeCompaction(scenario.reply);
  emit({ type: "summarization_retry_scheduled", attempt: 1, maxAttempts: 3, delayMs: 2000, errorMessage: scenario.error });
  emit({ type: "summarization_retry_attempt_start", source: "compaction", reason: "threshold" });
  emit({ type: "summarization_retry_finished" });
  completeCompaction(scenario.summary);
  settleRun();
}

function startLiveCustomMessages() {
  const scenario = live.customMessages;
  const timestamp = Date.now();
  const shown = { role: "custom", customType: scenario.customType, content: scenario.shown, display: true, timestamp };
  emitCustomMessage(shown);
  emitCustomMessage({ role: "custom", customType: scenario.customType, content: [{ type: "text", text: scenario.hidden }], display: false, timestamp });
  // Pi's custom messages have no ID, so a repeat of the same note is told apart by its timestamp.
  emitCustomMessage({ ...shown, timestamp: timestamp + 1000 });
  holdLiveScenario(scenario.gate, () => completeAssistant(scenario.reply));
}

function emitCustomMessage(message) {
  appendEntry("custom_message", { customType: message.customType, content: message.content, display: message.display });
  emitMessage(message);
}

function startLiveExtensionUi() {
  const scenario = live.extensionUi;
  const { above, implicit, below } = scenario.widgets;
  emitExtensionUi({ method: "setWidget", widgetKey: above.key, widgetLines: above.lines, widgetPlacement: "aboveEditor" });
  emitExtensionUi({ method: "setWidget", widgetKey: implicit.key, widgetLines: implicit.lines });
  emitExtensionUi({ method: "setWidget", widgetKey: below.key, widgetLines: below.lines, widgetPlacement: "belowEditor" });
  emitExtensionUi({ method: "setTitle", title: scenario.title });
  emitExtensionUi({ method: "notify", message: scenario.notices.info, notifyType: "info" });
  emitExtensionUi({ method: "notify", message: scenario.notices.warning, notifyType: "warning" });
  emitExtensionUi({ method: "notify", message: scenario.notices.error, notifyType: "error" });
  emitExtensionUi({ method: "set_editor_text", text: scenario.editorText.empty });
  holdLiveScenario(scenario.gates.removal, () => {
    // Pi leaves out widget lines and the title when an extension clears them.
    emitExtensionUi({ method: "setWidget", widgetKey: implicit.key });
    emitExtensionUi({ method: "setTitle" });
    emitExtensionUi({ method: "set_editor_text", text: scenario.editorText.replaced });
    holdLiveScenario(scenario.gates.decline, () => {
      emitExtensionUi({ method: "set_editor_text", text: scenario.editorText.declined });
      completeAssistant(scenario.reply);
    });
  });
}

function startRestoredExtensionUi() {
  const scenario = live.extensionUi;
  const { above, below } = scenario.widgets;
  emitExtensionUi({ method: "setWidget", widgetKey: above.key, widgetLines: above.lines, widgetPlacement: "aboveEditor" });
  emitExtensionUi({ method: "setWidget", widgetKey: below.key, widgetLines: below.lines, widgetPlacement: "belowEditor" });
  emitExtensionUi({ method: "setTitle", title: scenario.title });
  holdLiveScenario(scenario.gates.restore, () => completeAssistant(scenario.restoreReply));
}

function emitExtensionUi(fields) {
  emit({ type: "extension_ui_request", id: randomUUID(), ...fields });
}

// The first pause lets the status refresh after the user message finish before the footer changes.
function startLiveFooter() {
  const scenario = live.footer;
  holdLiveScenario(scenario.gates.start, () => {
    const started = { ...assistantMessage([], "stop"), provider: scenario.provider, model: scenario.model };
    emit({ type: "message_start", message: started });
    thinkingLevel = scenario.thinkingLevel;
    appendEntry("thinking_level_change", { thinkingLevel });
    emit({ type: "thinking_level_changed", level: thinkingLevel });
    holdLiveScenario(scenario.gates.streaming, () => {
      const completed = { ...started, content: [{ type: "text", text: scenario.reply }] };
      emit({ type: "message_end", message: completed });
      appendMessage(completed);
      holdLiveScenario(scenario.gates.ended, () => {
        // No pause follows the rename: the sidebar doesn't refresh while a dialog is open.
        sessionName = scenario.renamed;
        appendEntry("session_info", { name: sessionName });
        emit({ type: "session_info_changed", name: sessionName });
        emit({ type: "turn_end", message: completed, toolResults: [] });
        emit({ type: "agent_end", messages: [completed], willRetry: false });
        settleRun();
      });
    });
  });
}

function startFailedCompaction() {
  const scenario = live.compaction;
  replyBeforeCompaction(scenario.reply);
  holdLiveScenario(scenario.failureGate, () => {
    emit({ type: "compaction_end", reason: "threshold", aborted: false, willRetry: false, errorMessage: scenario.error });
    settleRun();
  });
}

function startAbortedCompaction() {
  replyBeforeCompaction(live.compaction.reply);
  liveAbort = () => {
    emit({ type: "compaction_end", reason: "threshold", aborted: true, willRetry: false });
    settleRun({ aborted: true });
  };
}

function startDuplicateCompaction() {
  const scenario = live.compaction;
  replyBeforeCompaction(scenario.reply);
  completeCompaction(scenario.summary);
  // Older Pi versions also sent a legacy compaction event with the same summary.
  emit({ type: "compaction", summary: scenario.summary });
  emit({ type: "compaction", summary: scenario.summary, timestamp: new Date(Date.now() + 11_000).toISOString() });
  holdLiveScenario(scenario.duplicateGate, () => settleRun());
}

// Like Pi's automatic compaction, which runs after agent_end and before the run settles.
function replyBeforeCompaction(text) {
  const reply = emitAssistantReply(text);
  emit({ type: "turn_end", message: reply, toolResults: [] });
  emit({ type: "agent_end", messages: [reply], willRetry: false });
  emit({ type: "compaction_start", reason: "threshold" });
}

function completeCompaction(summary) {
  const result = { summary, firstKeptEntryId: leafId, tokensBefore: 150 };
  appendEntry("compaction", result);
  emit({ type: "compaction_end", reason: "threshold", result, aborted: false, willRetry: false });
}

function startFinishedThinking() {
  const scenario = live.thinking;
  const started = startThinking();
  holdLiveScenario(scenario.gates.thinking, () => {
    emit({ type: "message_update", assistantMessageEvent: { type: "thinking_end", contentIndex: 0, content: scenario.thought } });
    holdLiveScenario(scenario.gates.ended, () => {
      emit({ type: "message_update", assistantMessageEvent: { type: "text_start", contentIndex: 1 } });
      emit({ type: "message_update", assistantMessageEvent: { type: "text_delta", contentIndex: 1, delta: scenario.reply } });
      emit({ type: "message_update", assistantMessageEvent: { type: "text_end", contentIndex: 1, content: scenario.reply } });
      finishAssistant({ ...started, content: [{ type: "thinking", thinking: scenario.thought }, { type: "text", text: scenario.reply }] });
    });
  });
}

function startFailedThinking() {
  const scenario = live.thinking;
  const started = startThinking();
  holdLiveScenario(scenario.gates.failing, () => {
    finishAssistant({ ...started, content: [{ type: "thinking", thinking: scenario.thought }], stopReason: "error", errorMessage: scenario.error });
  });
}

function startAbortedThinking() {
  const started = startThinking();
  liveAbort = () => finishAssistant({ ...started, content: [{ type: "thinking", thinking: live.thinking.thought }], stopReason: "aborted" });
}

// Delta-only, like native Pi, so the gateway rebuilds the partial message.
function startThinking() {
  const started = assistantMessage([], "stop");
  emit({ type: "message_start", message: started });
  emit({ type: "message_update", assistantMessageEvent: { type: "thinking_start", contentIndex: 0 } });
  emit({ type: "message_update", assistantMessageEvent: { type: "thinking_delta", contentIndex: 0, delta: live.thinking.thought } });
  return started;
}

function emitAssistantReply(text) {
  const timestamp = Date.now();
  const completed = assistantMessage([{ type: "text", text }], "stop", timestamp);
  emit({ type: "message_start", message: assistantMessage([], "stop", timestamp) });
  emit({ type: "message_update", message: completed, assistantMessageEvent: { type: "text_delta", contentIndex: 0, delta: text, partial: completed } });
  emit({ type: "message_end", message: completed });
  appendMessage(completed);
  return completed;
}

function settleRun(fields = {}) {
  busy = false;
  activeScenario = null;
  persistDeferredBashMessages();
  emit({ type: "agent_settled", ...fields });
}

function replaceWithNewSession(command) {
  clearTimers();
  if (pendingMessages.length) clearPendingMessages();
  if (busy) {
    busy = false;
    activeScenario = null;
    emit({ type: "agent_end", messages: [], willRetry: false });
    emit({ type: "agent_settled" });
  }
  entries = [];
  leafId = null;
  sessionName = null;
  sessionPersisted = false;
  prepareNewSession();
  respond(command, true, { data: { cancelled: false } });
}

// Like Pi's SessionManager.forkFrom: a new file in the folder's session directory with every entry copied and a link back.
function forkSession(sourcePath) {
  loadSession(sourcePath);
  const timestamp = new Date().toISOString();
  const id = randomUUID();
  const directory = path.join(sessionsRoot, `--${process.cwd().replace(/^\//, "").replace(/[/\\:]/g, "-")}--`);
  sessionPath = path.join(directory, `${timestamp.replace(/[:.]/g, "-")}_${id}.jsonl`);
  header = { type: "session", version: 3, id, timestamp, cwd: process.cwd(), parentSession: sourcePath };
  mkdirSync(directory, { recursive: true });
  writeFileSync(sessionPath, `${[header, ...entries].map((record) => JSON.stringify(record)).join("\n")}\n`);
}

function prepareNewSession(filePath) {
  if (!sessionsRoot) throw new Error("GRIPI_E2E_SESSIONS_ROOT is required for new fake sessions");
  sessionPath = filePath || path.join(sessionsRoot, "e2e", `generated-${randomUUID()}.jsonl`);
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
  if (heldPromptEvents && !["response", "extension_ui_request"].includes(record.type)) {
    heldPromptEvents.push(record);
    return;
  }
  process.stdout.write(`${JSON.stringify(record)}\n`);
}

function schedule(delay, callback) {
  const timer = setTimeout(() => {
    timers.delete(timer);
    callback();
  }, delay);
  timers.add(timer);
  return timer;
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
