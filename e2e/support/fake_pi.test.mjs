import assert from "node:assert/strict";
import { once } from "node:events";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { spawn } from "node:child_process";
import test from "node:test";
import { seedFixtures } from "../fixtures/seed.mjs";
import { nativeBash, prompts } from "./contract.mjs";

const fakePiPath = path.resolve("e2e/support/fake_pi.mjs");

test("fake Pi correlates RPC responses and returns native entries", { timeout: 5_000 }, async (context) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "gripi-fake-pi-test-"));
  const fixture = await seedFixtures(root);
  const sessionPath = path.join(fixture.sessionsRoot, "e2e", "prompt.jsonl");
  const child = spawnFake(fixture, ["--mode", "rpc", "--session", sessionPath]);
  context.after(() => cleanup(child, root));
  const records = recordsFrom(child.stdout);

  child.stdin.write(`${JSON.stringify({ id: "state-1", type: "get_state" })}\n`);
  child.stdin.write(`${JSON.stringify({ id: "entries-1", type: "get_entries" })}\n`);

  const state = await nextRecord(records);
  const entries = await nextRecord(records);
  assert.equal(state.id, "state-1");
  assert.equal(state.data.sessionFile, sessionPath);
  assert.equal(entries.id, "entries-1");
  assert.equal(entries.success, true);
  assert.ok(entries.data.entries.some((entry) => entry.type === "session_info"));
});

test("fake Pi follows the native prompt lifecycle for a new session", { timeout: 5_000 }, async (context) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "gripi-fake-pi-prompt-"));
  const fixture = await seedFixtures(root);
  const child = spawnFake(fixture, ["--mode", "rpc"], fixture.projects["new-session-desktop"]);
  context.after(() => cleanup(child, root));
  const records = recordsFrom(child.stdout);

  child.stdin.write(`${JSON.stringify({ id: "abort-idle", type: "abort" })}\n`);
  assert.equal((await nextRecord(records)).success, true);
  child.stdin.write(`${JSON.stringify({ id: "state-new", type: "get_state" })}\n`);
  const state = await nextRecord(records);
  const sessionPath = state.data.sessionFile;
  await assert.rejects(readFile(sessionPath), { code: "ENOENT" });

  child.stdin.write(`${JSON.stringify({ id: "prompt-held", type: "prompt", message: prompts.abortStart })}\n`);
  assert.equal((await nextRecord(records)).id, "prompt-held");
  const heldEvents = await readRecords(records, 4);
  assert.deepEqual(heldEvents.map((event) => [event.type, event.message?.role]), [
    ["agent_start", undefined],
    ["message_start", "user"],
    ["message_end", "user"],
    ["turn_start", undefined]
  ]);
  await assert.rejects(readFile(sessionPath), { code: "ENOENT" });

  child.stdin.write(`${JSON.stringify({ id: "abort-active", type: "abort" })}\n`);
  assert.equal((await nextRecord(records)).id, "abort-active");
  assert.deepEqual((await readRecords(records, 2)).map((event) => event.type), ["agent_end", "agent_settled"]);

  child.stdin.write(`${JSON.stringify({ id: "prompt-new", type: "prompt", message: prompts.newSession })}\n`);
  assert.equal((await nextRecord(records)).id, "prompt-new");
  const events = [];
  while (events.at(-1)?.type !== "agent_settled") events.push(await nextRecord(records));

  assert.ok(events.some((event) => event.type === "message_end" && event.message?.role === "toolResult"));
  assert.ok(events.some((event) => event.type === "message_end" && event.message?.role === "assistant"));
  const persisted = (await readFile(sessionPath, "utf8")).trim().split("\n").map((line) => JSON.parse(line));
  assert.equal(persisted[0].type, "session");
  assert.deepEqual(persisted.filter((entry) => entry.type === "message").map((entry) => entry.message.role), ["user", "user", "assistant", "toolResult", "assistant"]);
});

test("fake Pi keeps fresh bash history in memory until an assistant response persists the session", { timeout: 5_000 }, async (context) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "gripi-fake-pi-bash-"));
  const fixture = await seedFixtures(root);
  const child = spawnFake(fixture, ["--mode", "rpc"], fixture.projects["bash-project"]);
  context.after(() => cleanup(child, root));
  const records = recordsFrom(child.stdout);
  child.stdin.write(`${JSON.stringify({ id: "bash-state", type: "get_state" })}\n`);
  const sessionPath = (await nextRecord(records)).data.sessionFile;
  await assert.rejects(readFile(sessionPath), { code: "ENOENT" });

  child.stdin.write(`${JSON.stringify({ id: "bash-included", type: "bash", command: nativeBash.included.command })}\n`);
  const included = await nextRecord(records);
  assert.deepEqual(included, {
    id: "bash-included",
    type: "response",
    command: "bash",
    success: true,
    data: { output: nativeBash.included.output, exitCode: 0, cancelled: false, truncated: false }
  });

  child.stdin.write(`${JSON.stringify({ id: "bash-excluded", type: "bash", command: nativeBash.excluded.command, excludeFromContext: true })}\n`);
  assert.equal((await nextRecord(records)).id, "bash-excluded");
  child.stdin.write(`${JSON.stringify({ id: "bash-nonzero", type: "bash", command: nativeBash.nonzero.command })}\n`);
  const nonzero = await nextRecord(records);
  assert.equal(nonzero.data.exitCode, nativeBash.nonzero.exitCode);
  assert.equal(nonzero.data.output, nativeBash.nonzero.output);

  await assert.rejects(readFile(sessionPath), { code: "ENOENT" });

  child.stdin.write(`${JSON.stringify({ id: "prompt-after-bash", type: "prompt", message: prompts.standard })}\n`);
  assert.equal((await nextRecord(records)).id, "prompt-after-bash");
  const promptEvents = [];
  while (promptEvents.at(-1)?.type !== "agent_settled") promptEvents.push(await nextRecord(records));
  assert.ok(promptEvents.some((event) => event.type === "message_end" && event.message?.role === "assistant"));

  const persisted = (await readFile(sessionPath, "utf8")).trim().split("\n").map((line) => JSON.parse(line));
  assert.equal(persisted[0].type, "session");
  const messages = persisted.filter((entry) => entry.type === "message").map((entry) => entry.message);
  const bashMessages = messages.filter((message) => message.role === "bashExecution");
  assert.deepEqual(bashMessages.map((message) => ({
    command: message.command,
    output: message.output,
    exitCode: message.exitCode,
    cancelled: message.cancelled,
    truncated: message.truncated,
    excludeFromContext: message.excludeFromContext,
    timestampType: typeof message.timestamp
  })), [
    { ...nativeBash.included, exitCode: 0, cancelled: false, truncated: false, excludeFromContext: undefined, timestampType: "number" },
    { ...nativeBash.excluded, exitCode: 0, cancelled: false, truncated: false, excludeFromContext: true, timestampType: "number" },
    { command: nativeBash.nonzero.command, output: nativeBash.nonzero.output, exitCode: 7, cancelled: false, truncated: false, excludeFromContext: undefined, timestampType: "number" }
  ]);
});

test("fake Pi serializes bash, cancels it while the agent runs, and defers its entry", { timeout: 5_000 }, async (context) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "gripi-fake-pi-bash-overlap-"));
  const fixture = await seedFixtures(root);
  const sessionPath = path.join(fixture.sessionsRoot, "e2e", "bash-overlap.jsonl");
  const child = spawnFake(fixture, ["--mode", "rpc", "--session", sessionPath]);
  context.after(() => cleanup(child, root));
  const records = recordsFrom(child.stdout);

  child.stdin.write(`${JSON.stringify({ id: "prompt-active", type: "prompt", message: prompts.abortStart })}\n`);
  assert.equal((await nextRecord(records)).id, "prompt-active");
  await readRecords(records, 4);

  child.stdin.write(`${JSON.stringify({ id: "bash-long", type: "bash", command: nativeBash.overlap.command })}\n`);
  child.stdin.write(`${JSON.stringify({ id: "bash-duplicate", type: "bash", command: nativeBash.included.command })}\n`);
  const duplicate = await nextRecord(records);
  assert.equal(duplicate.id, "bash-duplicate");
  assert.equal(duplicate.success, false);
  assert.match(duplicate.error, /already running/i);

  child.stdin.write(`${JSON.stringify({ id: "entries-before", type: "get_entries" })}\n`);
  const before = await nextRecord(records);
  assert.equal(before.id, "entries-before");
  assert.equal(before.data.entries.some((entry) => entry.message?.role === "bashExecution"), false);

  child.stdin.write(`${JSON.stringify({ id: "cancel-long", type: "abort_bash" })}\n`);
  const cancellationRecords = await readRecords(records, 2);
  const abortResponse = cancellationRecords.find((record) => record.id === "cancel-long");
  const bashResponse = cancellationRecords.find((record) => record.id === "bash-long");
  assert.equal(abortResponse.success, true);
  assert.deepEqual(bashResponse.data, { output: "", cancelled: true, truncated: false });

  child.stdin.write(`${JSON.stringify({ id: "entries-deferred", type: "get_entries" })}\n`);
  const deferred = await nextRecord(records);
  assert.equal(deferred.data.entries.some((entry) => entry.message?.role === "bashExecution"), false);

  child.stdin.write(`${JSON.stringify({ id: "abort-agent", type: "abort" })}\n`);
  assert.equal((await nextRecord(records)).id, "abort-agent");
  assert.deepEqual((await readRecords(records, 2)).map((record) => record.type), ["agent_end", "agent_settled"]);
  child.stdin.write(`${JSON.stringify({ id: "entries-settled", type: "get_entries" })}\n`);
  const settled = await nextRecord(records);
  const bashEntry = settled.data.entries.at(-1);
  assert.equal(bashEntry.message.role, "bashExecution");
  assert.equal(bashEntry.message.command, nativeBash.overlap.command);
  assert.equal(bashEntry.message.cancelled, true);
});

test("fake Pi supports model, tree, compaction, and branch control contracts", { timeout: 5_000 }, async (context) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "gripi-fake-pi-controls-"));
  const fixture = await seedFixtures(root);
  const sessionPath = path.join(fixture.sessionsRoot, "e2e", "prompt.jsonl");
  const child = spawnFake(fixture, ["--mode", "rpc", "--session", sessionPath]);
  context.after(() => cleanup(child, root));
  const records = recordsFrom(child.stdout);

  child.stdin.write(`${JSON.stringify({ id: "models", type: "get_available_models" })}\n`);
  assert.ok((await nextRecord(records)).data.models.some((model) => model.id === "contract-model"));
  child.stdin.write(`${JSON.stringify({ id: "compact", type: "compact", customInstructions: "Keep decisions" })}\n`);
  assert.equal((await nextRecord(records)).id, "compact");
  assert.equal((await nextRecord(records)).type, "compaction_end");

  const requestId = "abc123";
  const payload = Buffer.from(JSON.stringify({ filter: "all" })).toString("base64url");
  child.stdin.write(`${JSON.stringify({ id: "tree", type: "prompt", message: `/gripi_tree_snapshot ${requestId} ${payload}` })}\n`);
  assert.equal((await nextRecord(records)).id, "tree");
  const tree = await nextRecord(records);
  assert.equal(tree.method, "setStatus");
  assert.equal(tree.statusKey, `gripi_tree_snapshot:${requestId}`);
  assert.ok(JSON.parse(tree.statusText).entries.length > 0);

  child.stdin.write(`${JSON.stringify({ id: "clone", type: "clone" })}\n`);
  assert.equal((await nextRecord(records)).success, true);
  child.stdin.write(`${JSON.stringify({ id: "cloned-state", type: "get_state" })}\n`);
  assert.notEqual((await nextRecord(records)).data.sessionFile, sessionPath);
});

test("fake Pi uses LF framing rather than Unicode line separators", { timeout: 5_000 }, async (context) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "gripi-fake-pi-framing-"));
  const fixture = await seedFixtures(root);
  const sessionPath = path.join(fixture.sessionsRoot, "e2e", "prompt.jsonl");
  const child = spawnFake(fixture, ["--mode", "rpc", "--session", sessionPath]);
  context.after(() => cleanup(child, root));
  const records = recordsFrom(child.stdout);
  const command = JSON.stringify({ id: "unknown-1", type: `unknown\u2028command` }).replace("\\u2028", "\u2028");
  child.stdin.write(`${command}\n`);

  const response = await nextRecord(records);
  assert.equal(response.id, "unknown-1");
  assert.equal(response.command, "unknown\u2028command");
  assert.equal(response.success, false);
});

function spawnFake(fixture, args, cwd) {
  return spawn(process.execPath, [fakePiPath, ...args], {
    cwd,
    env: { ...process.env, GRIPI_E2E_SESSIONS_ROOT: fixture.sessionsRoot },
    stdio: ["pipe", "pipe", "pipe"]
  });
}

async function cleanup(child, root) {
  if (child.exitCode === null) {
    child.kill("SIGTERM");
    await once(child, "exit");
  }
  await rm(root, { recursive: true, force: true });
}

async function nextRecord(records) {
  let timer;
  try {
    const result = await Promise.race([
      records.next(),
      new Promise((_, reject) => { timer = setTimeout(() => reject(new Error("Timed out waiting for fake Pi output")), 2_000); })
    ]);
    assert.equal(result.done, false, "Fake Pi exited before emitting the expected record");
    return result.value;
  } finally {
    clearTimeout(timer);
  }
}

async function readRecords(records, count) {
  const values = [];
  while (values.length < count) values.push(await nextRecord(records));
  return values;
}

async function* recordsFrom(stream) {
  let buffer = "";
  stream.setEncoding("utf8");
  for await (const chunk of stream) {
    buffer += chunk;
    while (true) {
      const newline = buffer.indexOf("\n");
      if (newline === -1) break;
      const line = buffer.slice(0, newline);
      buffer = buffer.slice(newline + 1);
      if (line) yield JSON.parse(line);
    }
  }
}
