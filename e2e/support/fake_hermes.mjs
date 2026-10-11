#!/usr/bin/env node

// Stands in for `hermes acp` in tests: answers the Agent Client Protocol on standard input and output like Hermes Agent.
import { appendFileSync, existsSync, readFileSync, writeFileSync } from "node:fs";
import { createInterface } from "node:readline";
import { hermes } from "./contract.mjs";

if (process.argv[2] !== "acp") {
  console.error(`usage: fake_hermes.mjs acp (got: ${process.argv.slice(2).join(" ")})`);
  process.exit(2);
}

// FAKE_HERMES_LOG receives how the fake was started and each message it got, one JSON record per line.
function log(record) {
  if (process.env.FAKE_HERMES_LOG) appendFileSync(process.env.FAKE_HERMES_LOG, `${JSON.stringify(record)}\n`);
}
log({
  argv: process.argv.slice(2),
  sessionSource: process.env.HERMES_SESSION_SOURCE ?? null,
  probe: process.env.FAKE_HERMES_PROBE ?? null,
  gripiSettings: Object.keys(process.env).filter((key) => key.startsWith("GRIPI_") && !key.startsWith("GRIPI_E2E_")).sort()
});
// Hermes logs its startup on standard error, and the gateway must keep reading it.
for (let step = 1; step <= 40; step++) console.error(`INFO hermes.acp: fake start-up, step ${step} of 40`);

const capabilities = process.env.FAKE_HERMES_CAPABILITIES
  ? JSON.parse(process.env.FAKE_HERMES_CAPABILITIES)
  : { loadSession: true, promptCapabilities: { image: true }, sessionCapabilities: { fork: {}, list: {}, resume: {} } };
const MODEL = "anthropic:claude-opus-4-5";
const PAGE_SIZE = 50;

// FAKE_HERMES_STATE names a JSON file of saved sessions: {"sessions": [{"sessionId", "cwd", "title", "updatedAt",
// "model", "history"}]}, where history holds the ACP updates that session/load replays. Like Hermes, the fake saves a
// new session once it has a message.
const statePath = process.env.FAKE_HERMES_STATE;
const sessions = new Map((statePath && existsSync(statePath) ? JSON.parse(readFileSync(statePath, "utf8")).sessions ?? [] : []).map((session) => [session.sessionId, { ...session, saved: true }]));
let created = 0;

function save(session) {
  session.saved = true;
  session.updatedAt = new Date().toISOString();
  if (!statePath) return;
  const saved = [...sessions.values()].filter((candidate) => candidate.saved).map(({ saved: _saved, running: _running, ...rest }) => rest);
  writeFileSync(statePath, JSON.stringify({ sessions: saved }));
}

function send(message) {
  process.stdout.write(`${JSON.stringify({ jsonrpc: "2.0", ...message })}\n`);
}

function update(sessionId, value) {
  send({ method: "session/update", params: { sessionId, update: value } });
}

function text(kind, value, messageId) {
  return { sessionUpdate: kind, content: { type: "text", text: value }, ...(messageId ? { messageId } : {}) };
}

function toolContent(value) {
  return [{ type: "content", content: { type: "text", text: value } }];
}

function modelAnswer(session) {
  const model = session.model ?? MODEL;
  return { models: { currentModelId: model, availableModels: [{ modelId: model, name: model }] }, modes: { currentModeId: session.mode ?? "default", availableModes: [] } };
}

let requested = 0;
const answers = new Map();

// Asks the client like Hermes does before a dangerous command, and resolves with the option the client selected.
function requestPermission(sessionId, toolCall, options, timeout) {
  requested += 1;
  const id = requested;
  send({ id, method: "session/request_permission", params: { sessionId, toolCall, options } });
  return new Promise((resolve) => {
    answers.set(id, resolve);
    if (timeout !== undefined) setTimeout(() => answers.delete(id) && resolve({}), timeout);
  });
}

// The scripted run of a prompt: each step after the one before. A prompt asking to wait holds until session/cancel, and
// a function step waits for what it returns.
function scenario(prompt, turn, sessionId) {
  const { permission } = hermes;
  if (prompt === hermes.exitPrompt) {
    return [text("agent_thought_chunk", "Exiting.", `thought-${turn}`), () => process.exit(3)];
  }
  if (prompt === permission.prompt || prompt === permission.expiringPrompt) {
    const id = `tc-${turn}`;
    return [
      { sessionUpdate: "tool_call", toolCallId: id, title: permission.tool, kind: "execute", content: toolContent("$ rm -rf build"), locations: [] },
      async () => {
        const options = [
          { optionId: "allow_once", kind: "allow_once", name: permission.allow },
          { optionId: "allow_session", kind: "allow_always", name: "Allow for session" },
          { optionId: "deny", kind: "reject_once", name: permission.deny }
        ];
        const check = { toolCallId: `perm-check-${turn}`, title: permission.title, kind: "execute", status: "pending", content: toolContent("recursive delete\n$ rm -rf build") };
        const { outcome } = await requestPermission(sessionId, check, options, prompt === permission.expiringPrompt ? 0 : undefined);
        const allowed = outcome?.outcome === "selected" && outcome.optionId === "allow_once";
        // Like Hermes, the check itself ends with an update for a call that was never announced.
        return [
          { sessionUpdate: "tool_call_update", toolCallId: check.toolCallId, kind: "execute", status: "completed", content: toolContent(allowed ? "allowed" : "denied") },
          { sessionUpdate: "tool_call_update", toolCallId: id, kind: "execute", status: allowed ? "completed" : "failed", content: toolContent(allowed ? permission.allowed : permission.denied) },
          text("agent_message_chunk", allowed ? "The build folder is gone." : "I left the build folder alone.", `reply-${turn}`)
        ];
      }
    ];
  }
  if (prompt === hermes.stopPrompt) {
    return [text("agent_thought_chunk", `${hermes.stopThought}\n\nStill waiting`, `thought-${turn}`), "hold"];
  }
  const { run } = hermes;
  const [thoughtStart, thoughtEnd] = [run.thought.slice(0, 9), run.thought.slice(9)];
  return [
    text("agent_thought_chunk", thoughtStart, `thought-${turn}`),
    text("agent_thought_chunk", thoughtEnd, `thought-${turn}`),
    text("agent_message_chunk", run.intro, `intro-${turn}`),
    { sessionUpdate: "tool_call", toolCallId: `tc-${turn}`, title: run.tool, kind: "execute", content: toolContent("$ ls"), locations: [] },
    { sessionUpdate: "tool_call_update", toolCallId: `tc-${turn}`, kind: "execute", status: "completed", content: toolContent(`${run.toolOutput}\nsrc`) },
    text("agent_message_chunk", run.replyPrefix, `reply-${turn}`),
    text("agent_message_chunk", prompt, `reply-${turn}`)
  ];
}

const running = new Map();

function runPrompt(session, request) {
  const prompt = (request.params.prompt ?? []).filter((block) => block.type === "text").map((block) => block.text).join("\n");
  session.history ??= [];
  session.history.push(text("user_message_chunk", prompt));
  session.title ??= prompt.slice(0, 40);
  save(session);
  const turn = session.history.filter((value) => value.sessionUpdate === "user_message_chunk").length;
  const steps = scenario(prompt, turn, session.sessionId);
  const finish = (stopReason) => {
    clearTimeout(run.timer);
    running.delete(session.sessionId);
    save(session);
    update(session.sessionId, { sessionUpdate: "usage_update", size: 200000, used: 31200 + turn * 1000 });
    send({ id: request.id, result: { stopReason, usage: { inputTokens: 10, outputTokens: 5, totalTokens: 15 } } });
  };
  const run = { finish, timer: null };
  running.set(session.sessionId, run);
  const next = async (index) => {
    if (index === steps.length) return finish("end_turn");
    if (steps[index] === "hold") return;
    if (typeof steps[index] === "function") {
      steps.splice(index + 1, 0, ...await steps[index]());
      return next(index + 1);
    }
    update(session.sessionId, steps[index]);
    session.history.push(steps[index]);
    run.timer = setTimeout(() => next(index + 1), Number(process.env.FAKE_HERMES_STEP_MS ?? 30));
  };
  next(0);
}

const methods = {
  initialize: () => ({ protocolVersion: 1, agentCapabilities: capabilities, agentInfo: { name: "hermes-agent", version: "0.0.0-fake" }, authMethods: [] }),
  // Like Hermes: only saved sessions, the most recently updated first, in pages that continue after the cursor's session.
  "session/list": ({ cursor } = {}) => {
    const listed = [...sessions.values()].filter((session) => session.saved).sort((left, right) => right.updatedAt.localeCompare(left.updatedAt));
    const start = cursor ? listed.findIndex((session) => session.sessionId === cursor) + 1 : 0;
    const page = listed.slice(start, start + PAGE_SIZE).map(({ sessionId, cwd, title, updatedAt }) => ({ sessionId, cwd, title: title ?? "New thread", updatedAt }));
    return start + PAGE_SIZE < listed.length ? { sessions: page, nextCursor: page.at(-1).sessionId } : { sessions: page };
  },
  "session/new": ({ cwd }) => {
    created += 1;
    const session = { sessionId: `fake-${process.pid}-${created}`, cwd, title: null, history: [], updatedAt: new Date().toISOString(), saved: false };
    sessions.set(session.sessionId, session);
    return { sessionId: session.sessionId, ...modelAnswer(session) };
  },
  // Like Hermes: the transcript replays before the answer, and an unknown session answers nothing.
  "session/load": ({ sessionId }) => {
    const session = sessions.get(sessionId);
    if (!session) return {};
    for (const value of session.history ?? []) update(sessionId, value);
    setImmediate(() => update(sessionId, { sessionUpdate: "usage_update", size: 200000, used: session.contextUsed ?? 31200 }));
    return modelAnswer(session);
  },
  "session/set_mode": ({ sessionId, modeId }) => {
    const session = sessions.get(sessionId);
    if (session) session.mode = modeId;
    return {};
  }
};

createInterface({ input: process.stdin }).on("line", (line) => {
  if (!line.trim()) return;
  const request = JSON.parse(line);
  log({ method: request.method, params: request.params, ...(request.method ? {} : { id: request.id, result: request.result }) });
  if (!request.method && answers.has(request.id)) {
    answers.get(request.id)(request.result ?? {});
    answers.delete(request.id);
    return;
  }
  if (request.method === "session/cancel") {
    running.get(request.params.sessionId)?.finish("cancelled");
    return;
  }
  if (request.id === undefined) return;
  if (request.method === "session/prompt") {
    const session = sessions.get(request.params.sessionId);
    if (!session) return send({ id: request.id, result: { stopReason: "refusal" } });
    runPrompt(session, request);
    return;
  }
  const method = methods[request.method];
  if (!method) {
    send({ id: request.id, error: { code: -32601, message: `Method not found: ${request.method}` } });
    return;
  }
  send({ id: request.id, result: method(request.params) });
});
