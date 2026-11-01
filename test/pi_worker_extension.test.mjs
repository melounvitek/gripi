import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { existsSync, mkdtempSync } from "node:fs";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import net from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import { test as nodeTest } from "node:test";
import { setTimeout as sleep } from "node:timers/promises";

const piExecutable = process.env.GRIPI_PI || execFileSync("sh", ["-c", "command -v pi"], { encoding: "utf8" }).trim();
// Workers need Pi 0.99.0 or later (MinimumPiVersion in internal/worker/auth), so older ones skip these tests.
const piVersion = execFileSync(piExecutable, ["--version"], {
  encoding: "utf8",
  env: { ...process.env, PI_CODING_AGENT_DIR: mkdtempSync(path.join(tmpdir(), "gripi-pi-version-")) },
}).trim();
const [major, minor] = (piVersion.match(/(\d+)\.(\d+)\.\d+/) || [0, 0, 0]).slice(1).map(Number);
const skip = major === 0 && minor < 99 ? `Pi ${piVersion} is older than 0.99.0, which workers need` : false;
const test = (name, options, fn) => nodeTest(name, { ...options, skip }, fn);
const extensionPath = path.resolve("pi_extensions/gripi-worker.ts");
const providerPath = path.resolve("test/helpers/fake_provider.ts");
const bash = (command) => ({ name: "bash", arguments: { command } });
const ok = async () => ({ ok: true });
const never = () => new Promise(() => {});

test("holds a tool result until the helper acknowledges a barrier", { timeout: 30_000 }, async (t) => {
  const { helper, pi } = await setup(t);
  helper.answer = async () => {
    await sleep(500);
    return { ok: true };
  };

  const results = await pi.run([[bash("echo held")]]);

  assert.deepEqual(results, [{ id: "call_0_0", isError: false, text: "held\n" }]);
  assert.deepEqual(helper.requests.map(({ type }) => type), ["barrier"]);
  assert.ok(pi.toolEnd("call_0_0").at >= helper.replies[0].at);
});

test("fails a tool result closed when the helper refuses the barrier", { timeout: 30_000 }, async (t) => {
  const { helper, pi } = await setup(t);
  helper.answer = async () => ({ ok: false, error: "main could not apply the changes" });

  const [result] = await pi.run([[bash("echo refused")]]);

  assert.equal(result.isError, true);
  assert.match(result.text, /^\[Gripi\] .*main could not apply the changes/);
  assert.match(result.text, /refused\n$/);
});

test("fails a tool result closed when the helper doesn't answer in time", { timeout: 30_000 }, async (t) => {
  const { helper, pi } = await setup(t, { env: { GRIPI_WORKER_TIMEOUT_MS: "300" } });
  helper.answer = never;

  const [result] = await pi.run([[bash("echo slow")]]);

  assert.equal(result.isError, true);
  assert.match(result.text, /^\[Gripi\] .*no answer/);
});

test("fails a tool result closed when the helper socket is missing", { timeout: 30_000 }, async (t) => {
  const { pi } = await setup(t, { helper: false });

  const [result] = await pi.run([[bash("echo alone")]]);

  assert.equal(result.isError, true);
  assert.match(result.text, /^\[Gripi\] /);
});

test("holds parallel tool calls independently and covers waiting calls with the next barrier", { timeout: 30_000 }, async (t) => {
  const { directory, helper, pi } = await setup(t);
  const project = path.join(directory, "project");
  const acknowledgements = [];
  helper.answer = () => new Promise((resolve) => acknowledgements.push(() => resolve({ ok: true })));
  const afterGo = (name) => bash(`until [ -e go ]; do sleep 0.02; done; echo ${name}; touch ${name}-done`);

  const run = pi.run([[bash("echo a"), afterGo("b"), afterGo("c")]]);
  await helper.waitForRequest(1);
  await writeFile(path.join(project, "go"), "");
  await eventually(() => existsSync(path.join(project, "b-done")) && existsSync(path.join(project, "c-done")));
  await sleep(200); // lets b and c reach their hold while the first barrier is in flight
  acknowledgements[0]();
  await pi.waitFor((event) => event.type === "tool_execution_end" && event.toolCallId === "call_0_0");
  await helper.waitForRequest(2);

  assert.equal(pi.toolEnd("call_0_1"), undefined);
  assert.equal(pi.toolEnd("call_0_2"), undefined);
  acknowledgements[1]();
  const results = await run;
  assert.deepEqual(results.map(({ isError }) => isError), [false, false, false]);
  assert.deepEqual(helper.requests.map(({ type }) => type), ["barrier", "barrier"]);
});

test("holds a call made by another tool only through the outer call", { timeout: 30_000 }, async (t) => {
  const { helper, pi } = await setup(t);

  const results = await pi.run([[{ name: "nested", arguments: { command: "echo inner" } }]]);

  assert.deepEqual(results, [{ id: "call_0_0", isError: false, text: "inner\n" }]);
  assert.deepEqual(helper.requests.map(({ type }) => type), ["barrier"]);
});

test("keeps holding an aborted tool result for a grace period", { timeout: 30_000 }, async (t) => {
  const { directory, helper, pi } = await setup(t);
  helper.answer = async () => {
    await sleep(1000);
    return { ok: true };
  };

  await pi.send({ type: "prompt", message: `RUN ${JSON.stringify([[bash("echo late")]])}` });
  await helper.waitForRequest(1);
  await pi.send({ type: "abort" });
  const late = pi.toolEnd("call_0_0");
  assert.equal(late.isError, false);
  assert.ok(late.at >= helper.replies[0].at);

  helper.answer = never;
  await pi.send({ type: "prompt", message: `RUN ${JSON.stringify([[bash("echo lost")]])}` });
  await helper.waitForRequest(2);
  const abortedAt = Date.now();
  await pi.send({ type: "abort" });
  const lost = pi.events.findLast((event) => event.type === "tool_execution_end");
  assert.equal(lost.isError, true);
  assert.match(lost.result.content[0].text, /^\[Gripi\] .*abort/i);
  assert.ok(lost.at - abortedAt >= 4500, `released ${lost.at - abortedAt} ms after the abort`);
  assert.ok(lost.at - abortedAt < 7000, `released ${lost.at - abortedAt} ms after the abort`);

  // The lost result blocks the next call until a health check, which an abort cuts short.
  await pi.send({ type: "prompt", message: `RUN ${JSON.stringify([[bash("touch after-abort")]])}` });
  await helper.waitForRequest(3);
  assert.equal(helper.requests[2].type, "health");
  const healthAbortedAt = Date.now();
  await pi.send({ type: "abort" });
  assert.ok(Date.now() - healthAbortedAt < 2000);
  assert.equal(existsSync(path.join(directory, "project", "after-abort")), false);
});

test("blocks tool calls after a failed acknowledgement until the helper reports health", { timeout: 30_000 }, async (t) => {
  const { directory, helper, pi } = await setup(t);
  helper.answer = async (request) => request.type === "barrier" ? "close" : { ok: false };

  const failed = await pi.run([[bash("echo first")], [bash("touch blocked")]]);

  assert.equal(failed[0].isError, true);
  assert.equal(failed[1].isError, true);
  assert.match(failed[1].text, /\[Gripi\] Not run/);
  assert.equal(existsSync(path.join(directory, "project", "blocked")), false);

  helper.answer = ok;
  const recovered = await pi.run([[bash("touch allowed")]]);

  assert.equal(recovered[0].isError, false);
  assert.equal(existsSync(path.join(directory, "project", "allowed")), true);
  assert.deepEqual(helper.requests.slice(-2).map(({ type }) => type), ["health", "barrier"]);
});

test("keeps tool calls blocked after a failure while a health check is pending", { timeout: 30_000 }, async (t) => {
  const { directory, helper, pi } = await setup(t);
  let reportHealthy;
  helper.answer = (request) => request.type === "barrier"
    ? { ok: false, error: "main could not apply the changes" }
    : new Promise((resolve) => { reportHealthy = () => resolve({ ok: true }); });

  const run = pi.run([[bash("echo first")], [bash("touch blocked")]]);
  await helper.waitForRequest(2);
  await pi.send({ type: "bash", command: "echo meanwhile" });
  reportHealthy();
  const results = await run;

  assert.match(results[1].text, /\[Gripi\] Not run/);
  assert.equal(existsSync(path.join(directory, "project", "blocked")), false);
});

test("holds ! commands and reports failures in their output", { timeout: 30_000 }, async (t) => {
  const { helper, pi } = await setup(t, { customShell: true });
  helper.answer = async () => {
    await sleep(500);
    return { ok: true };
  };

  const held = await pi.send({ type: "bash", command: "echo ran" });

  assert.ok(held.at >= helper.replies[0].at);
  assert.equal(held.data.exitCode, 0);
  assert.equal(held.data.output, "custom shell\nran\n");

  helper.answer = async () => ({ ok: false, error: "main could not apply the changes" });
  const failed = await pi.send({ type: "bash", command: "echo ran" });

  assert.equal(failed.success, true);
  assert.notEqual(failed.data.exitCode, 0);
  assert.match(failed.data.output, /^custom shell\nran\n\n\[Gripi\] .*main could not apply the changes/);
});

test("does nothing without GRIPI_WORKER_SOCKET", { timeout: 30_000 }, async (t) => {
  const { pi } = await setup(t, { helper: false, env: { GRIPI_WORKER_SOCKET: undefined } });

  const results = await pi.run([[bash("echo free")]]);
  const command = await pi.send({ type: "bash", command: "echo free" });

  assert.deepEqual(results, [{ id: "call_0_0", isError: false, text: "free\n" }]);
  assert.equal(command.data.exitCode, 0);
  assert.equal(command.data.output, "free\n");
});

async function eventually(condition) {
  while (!condition()) await sleep(20);
}

async function setup(t, { helper: withHelper = true, env = {}, customShell = false } = {}) {
  const directory = await mkdtemp(path.join(tmpdir(), "gripi-worker-extension-"));
  const project = path.join(directory, "project");
  const agent = path.join(directory, "agent");
  await mkdir(project);
  await mkdir(agent);
  if (customShell) {
    const shell = path.join(directory, "shell");
    await writeFile(shell, "#!/bin/sh\necho custom shell\nexec /bin/bash \"$@\"\n", { mode: 0o755 });
    await writeFile(path.join(agent, "settings.json"), JSON.stringify({ shellPath: shell }));
  }
  const socketPath = path.join(directory, "helper.sock");
  const helper = withHelper ? await startHelper(socketPath) : undefined;
  const pi = startPi(project, { PI_CODING_AGENT_DIR: agent, GRIPI_WORKER_SOCKET: socketPath, ...env });
  t.after(async () => {
    await pi.stop();
    await helper?.close();
    await rm(directory, { recursive: true, force: true });
  });
  return { directory, helper, pi };
}

// Plays Gripi's worker helper. `answer(request)` returns the reply's fields, "close" to drop the
// connection, or never settles to leave the request unanswered.
async function startHelper(socketPath) {
  const sockets = new Set();
  const waiters = [];
  const helper = {
    answer: ok,
    requests: [],
    replies: [],
    waitForRequest(count) {
      if (helper.requests.length >= count) return Promise.resolve();
      return new Promise((resolve) => waiters.push({ count, resolve }));
    },
    close() {
      for (const socket of sockets) socket.destroy();
      return new Promise((resolve) => server.close(resolve));
    },
  };
  const server = net.createServer((socket) => {
    sockets.add(socket);
    socket.on("close", () => sockets.delete(socket));
    socket.on("error", () => {});
    socket.setEncoding("utf8");
    let buffer = "";
    socket.on("data", (chunk) => {
      buffer += chunk;
      let newline;
      while ((newline = buffer.indexOf("\n")) >= 0) {
        const request = JSON.parse(buffer.slice(0, newline));
        buffer = buffer.slice(newline + 1);
        helper.requests.push({ ...request, at: Date.now() });
        for (const waiter of waiters.filter(({ count }) => helper.requests.length >= count)) {
          waiters.splice(waiters.indexOf(waiter), 1);
          waiter.resolve();
        }
        reply(socket, request);
      }
    });
  });
  async function reply(socket, request) {
    const answer = await helper.answer(request);
    if (answer === "close") return socket.destroy();
    helper.replies.push({ ...request, at: Date.now() });
    socket.write(`${JSON.stringify({ id: request.id, ...answer })}\n`);
  }
  await new Promise((resolve) => server.listen(socketPath, resolve));
  return helper;
}

function startPi(cwd, env) {
  const childEnv = { ...process.env, PI_OFFLINE: "1", PI_SKIP_VERSION_CHECK: "1", PI_TELEMETRY: "0", ...env };
  for (const [name, value] of Object.entries(childEnv)) if (value === undefined) delete childEnv[name];
  const child = spawn(piExecutable, [
    "--mode", "rpc", "--no-session", "-ne", "--approve",
    "--extension", providerPath, "--extension", extensionPath, "--model", "fake/fake-1",
  ], { cwd, env: childEnv, stdio: ["pipe", "pipe", "pipe"] });
  const exited = new Promise((resolve) => child.once("exit", resolve));
  const events = [];
  const waiters = [];
  let buffer = "";
  let stderr = "";
  child.stderr.on("data", (chunk) => { stderr += chunk; });
  child.stdout.setEncoding("utf8");
  child.stdout.on("data", (chunk) => {
    buffer += chunk;
    let newline;
    while ((newline = buffer.indexOf("\n")) >= 0) {
      const line = buffer.slice(0, newline);
      buffer = buffer.slice(newline + 1);
      if (!line.trim()) continue;
      const event = { ...JSON.parse(line), at: Date.now() };
      events.push(event);
      for (const waiter of waiters.filter(({ predicate }) => predicate(event))) {
        waiters.splice(waiters.indexOf(waiter), 1);
        waiter.resolve(event);
      }
    }
  });
  child.once("exit", () => {
    for (const waiter of waiters.splice(0)) waiter.reject(new Error(`Pi exited: ${stderr}`));
  });

  let nextId = 0;
  const pi = {
    events,
    waitFor(predicate, from = 0) {
      const found = events.slice(from).find(predicate);
      if (found) return Promise.resolve(found);
      return new Promise((resolve, reject) => waiters.push({ predicate, resolve, reject }));
    },
    send(command) {
      const id = `request-${++nextId}`;
      const response = pi.waitFor((event) => event.type === "response" && event.id === id, events.length);
      child.stdin.write(`${JSON.stringify({ ...command, id })}\n`);
      return response;
    },
    toolEnd(toolCallId) {
      return events.find((event) => event.type === "tool_execution_end" && event.toolCallId === toolCallId);
    },
    // Runs a scripted prompt and returns the tool results the model received.
    async run(steps) {
      const from = events.length;
      const settled = pi.waitFor((event) => event.type === "agent_settled", from);
      const response = await pi.send({ type: "prompt", message: `RUN ${JSON.stringify(steps)}` });
      assert.equal(response.success, true, response.error);
      await settled;
      const reply = events.slice(from).findLast((event) => event.type === "message_end" && event.message.role === "assistant");
      return JSON.parse(reply.message.content[0].text);
    },
    async stop() {
      child.stdin.end();
      const timer = setTimeout(() => child.kill("SIGKILL"), 3000);
      await exited;
      clearTimeout(timer);
    },
  };
  return pi;
}
