import assert from "node:assert/strict";
import { once } from "node:events";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { spawn } from "node:child_process";
import test from "node:test";
import { seedFixtures } from "../fixtures/seed.mjs";
import { prompts } from "./contract.mjs";

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
