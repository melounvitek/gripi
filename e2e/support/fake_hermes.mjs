#!/usr/bin/env node

// Stands in for `hermes acp` in tests: answers the Agent Client Protocol on standard input and output like Hermes Agent.
import { appendFileSync, existsSync, readFileSync } from "node:fs";
import { createInterface } from "node:readline";

if (process.argv[2] !== "acp") {
  console.error(`usage: fake_hermes.mjs acp (got: ${process.argv.slice(2).join(" ")})`);
  process.exit(2);
}

// FAKE_HERMES_LOG receives how the fake was started and each request it got, one JSON record per line.
function log(record) {
  if (process.env.FAKE_HERMES_LOG) appendFileSync(process.env.FAKE_HERMES_LOG, `${JSON.stringify(record)}\n`);
}
log({
  argv: process.argv.slice(2),
  sessionSource: process.env.HERMES_SESSION_SOURCE ?? null,
  gripiSettings: Object.keys(process.env).filter((key) => key.startsWith("GRIPI_") && !key.startsWith("GRIPI_E2E_")).sort()
});
// Hermes logs its startup on standard error, and the gateway must keep reading it.
for (let step = 1; step <= 40; step++) console.error(`INFO hermes.acp: fake start-up, step ${step} of 40`);

const capabilities = process.env.FAKE_HERMES_CAPABILITIES
  ? JSON.parse(process.env.FAKE_HERMES_CAPABILITIES)
  : { loadSession: true, promptCapabilities: { image: true }, sessionCapabilities: { fork: {}, list: {}, resume: {} } };

// FAKE_HERMES_STATE names a JSON file of saved sessions: {"sessions": [{"sessionId", "cwd", "title", "updatedAt",
// "model", "history"}]}, where history holds the ACP updates that session/load replays.
function savedSessions() {
  const path = process.env.FAKE_HERMES_STATE;
  return path && existsSync(path) ? JSON.parse(readFileSync(path, "utf8")).sessions : [];
}

const PAGE_SIZE = 50;

function update(sessionId, value) {
  send({ method: "session/update", params: { sessionId, update: value } });
}

const methods = {
  initialize: () => ({ protocolVersion: 1, agentCapabilities: capabilities, agentInfo: { name: "hermes-agent", version: "0.0.0-fake" }, authMethods: [] }),
  // Like Hermes: the most recently updated first, in pages that continue after the cursor's session.
  "session/list": ({ cursor } = {}) => {
    const sessions = savedSessions().sort((left, right) => right.updatedAt.localeCompare(left.updatedAt));
    const start = cursor ? sessions.findIndex((session) => session.sessionId === cursor) + 1 : 0;
    const page = sessions.slice(start, start + PAGE_SIZE).map(({ sessionId, cwd, title, updatedAt }) => ({ sessionId, cwd, title, updatedAt }));
    return start + PAGE_SIZE < sessions.length ? { sessions: page, nextCursor: page.at(-1).sessionId } : { sessions: page };
  },
  // Like Hermes: the transcript replays before the answer, and an unknown session answers nothing.
  "session/load": ({ sessionId }) => {
    const session = savedSessions().find((candidate) => candidate.sessionId === sessionId);
    if (!session) return {};
    for (const value of session.history ?? []) update(sessionId, value);
    const model = session.model ?? "anthropic:claude-opus-4-5";
    setImmediate(() => update(sessionId, { sessionUpdate: "usage_update", size: 200000, used: session.contextUsed ?? 31200 }));
    return { models: { currentModelId: model, availableModels: [{ modelId: model, name: model }] }, modes: { currentModeId: "default", availableModes: [] } };
  }
};

function send(message) {
  process.stdout.write(`${JSON.stringify({ jsonrpc: "2.0", ...message })}\n`);
}

createInterface({ input: process.stdin }).on("line", (line) => {
  if (!line.trim()) return;
  const request = JSON.parse(line);
  log({ method: request.method, params: request.params });
  if (request.id === undefined) return;
  const method = methods[request.method];
  if (!method) {
    send({ id: request.id, error: { code: -32601, message: `Method not found: ${request.method}` } });
    return;
  }
  send({ id: request.id, result: method(request.params) });
});
