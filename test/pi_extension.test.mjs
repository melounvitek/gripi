import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { existsSync } from "node:fs";
import { mkdir, mkdtemp, realpath, rm, symlink, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { test } from "node:test";

const piExecutable = process.env.GRIPI_PI || execFileSync("sh", ["-c", "command -v pi"], { encoding: "utf8" }).trim();
const piPackageRoot = await resolvePiPackageRoot(piExecutable);
// npm nests jiti inside a global Pi package but hoists it beside the package in Pi's managed install.
const jitiRoot = path.dirname(createRequire(path.join(piPackageRoot, "package.json")).resolve("jiti/package.json"));
const { createJiti } = await import(pathToFileURL(path.join(jitiRoot, "lib/jiti.mjs")));
const piIndex = path.join(piPackageRoot, "dist/index.js");
const extensionPath = path.resolve("pi_extensions/gripi-tree.ts");
const skillPath = path.resolve("pi_skills/gripi/SKILL.md");
const providerPath = path.resolve("test/helpers/fake_provider.ts");
// The scripted model provider needs Pi 0.99.0 or later.
const piVersion = execFileSync(piExecutable, ["--version"], { encoding: "utf8", env: { ...process.env, PI_SKIP_VERSION_CHECK: "1" } }).trim();
const [piMajor, piMinor] = (piVersion.match(/(\d+)\.(\d+)\.\d+/) || [0, 0, 0]).slice(1).map(Number);
const scriptedModelSkip = piMajor === 0 && piMinor < 99 ? `Pi ${piVersion} is older than 0.99.0, which the scripted model needs` : false;

async function resolvePiPackageRoot(executable) {
  const root = await packageRootAbove(executable) || await packageRootAbove(scriptBehindLauncher(executable));
  if (!root) throw new Error(`Cannot find Pi package root for ${executable}`);
  return root;
}

async function packageRootAbove(file) {
  let directory = path.dirname(await realpath(file));
  while (!existsSync(path.join(directory, "package.json"))) {
    const parent = path.dirname(directory);
    if (parent === directory) return null;
    directory = parent;
  }
  return directory;
}

// Pi's installer puts a shell launcher on PATH, outside the package. A Node preload reports the
// script the launcher ends up running; --version keeps Pi from starting if the preload is ignored.
function scriptBehindLauncher(launcher) {
  return execFileSync(launcher, ["--version"], {
    encoding: "utf8",
    timeout: 10_000,
    env: { ...process.env, NODE_OPTIONS: "--import=data:text/javascript,console.log(process.argv[1]);process.exit(0)" },
  }).trim();
}

for (const entrypoint of ["dist/cli.js", "dist/bundle/cli.js"]) {
  test(`resolves the Pi package root for ${entrypoint}`, async () => {
    const directory = await mkdtemp(path.join(tmpdir(), "gripi-pi-package-"));
    try {
      const packageRoot = path.join(directory, "pi-package");
      const cli = path.join(packageRoot, entrypoint);
      await mkdir(path.dirname(cli), { recursive: true });
      await writeFile(path.join(packageRoot, "package.json"), JSON.stringify({ type: "module" }));
      await writeFile(cli, "");
      const executable = path.join(directory, "pi");
      await symlink(cli, executable);

      assert.equal(await resolvePiPackageRoot(cli), packageRoot);
      assert.equal(await resolvePiPackageRoot(executable), packageRoot);
    } finally {
      await rm(directory, { recursive: true, force: true });
    }
  });
}

test("resolves the Pi package root behind a launcher script", async () => {
  const directory = await mkdtemp(path.join(tmpdir(), "gripi-pi-launcher-"));
  try {
    const packageRoot = path.join(directory, "release", "pi-package");
    const cli = path.join(packageRoot, "dist/cli.js");
    await mkdir(path.dirname(cli), { recursive: true });
    await writeFile(path.join(packageRoot, "package.json"), JSON.stringify({ type: "module" }));
    await writeFile(cli, "#!/usr/bin/env node\n", { mode: 0o755 });
    const launcher = path.join(directory, "pi");
    await writeFile(launcher, `#!/bin/sh\nexec "${cli}" "$@"\n`, { mode: 0o755 });

    assert.equal(await resolvePiPackageRoot(launcher), packageRoot);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("reload bridge refreshes native Pi resources in RPC mode", { timeout: 20_000 }, async () => {
  const directory = await mkdtemp(path.join(tmpdir(), "gripi-reload-extension-"));
  const project = path.join(directory, "project");
  const prompts = path.join(project, ".pi", "prompts");
  await mkdir(prompts, { recursive: true });
  const child = spawn(piExecutable, ["--mode", "rpc", "--no-session", "--extension", extensionPath, "--approve"], {
    cwd: project,
    env: { ...process.env, PI_CODING_AGENT_DIR: path.join(directory, "agent"), PI_SKIP_VERSION_CHECK: "1" },
    stdio: ["pipe", "pipe", "pipe"],
  });
  const records = rpcRecords(child);

  try {
    child.stdin.write(`${JSON.stringify({ id: "before", type: "get_commands" })}\n`);
    const before = await records.next((record) => record.id === "before");
    assert.ok(!before.data.commands.some((command) => command.name === "fresh-resource"));

    await writeFile(path.join(prompts, "fresh-resource.md"), "Fresh resource\n", "utf8");
    const reloadRequest = "abc";
    child.stdin.write(`${JSON.stringify({ id: "reload", type: "prompt", message: `/gripi_reload ${reloadRequest} e30` })}\n`);
    const [reload, status] = await Promise.all([
      records.next((record) => record.id === "reload"),
      records.next((record) => record.statusKey === `gripi_reload:${reloadRequest}`),
    ]);
    assert.equal(reload.success, true);
    assert.deepEqual(JSON.parse(status.statusText), { ok: true });

    child.stdin.write(`${JSON.stringify({ id: "after", type: "get_commands" })}\n`);
    const after = await records.next((record) => record.id === "after");
    assert.ok(after.data.commands.some((command) => command.name === "fresh-resource" && command.source === "prompt"));
  } finally {
    child.kill("SIGTERM");
    await new Promise((resolve) => child.once("exit", resolve));
    await rm(directory, { recursive: true, force: true });
  }
});

// Pi keeps the first skill of a name, and the skills it finds come before those passed with --skill.
test("Pi loads Gripi's skill unless the user has a skill of that name", { timeout: 20_000 }, async () => {
  const directory = await mkdtemp(path.join(tmpdir(), "gripi-skill-"));
  const agent = path.join(directory, "agent");
  const gripiSkill = async () => {
    const child = spawn(piExecutable, ["--mode", "rpc", "--no-session", "--skill", skillPath], {
      cwd: directory,
      env: { ...process.env, PI_CODING_AGENT_DIR: agent, PI_SKIP_VERSION_CHECK: "1" },
      stdio: ["pipe", "pipe", "pipe"],
    });
    const records = rpcRecords(child);
    try {
      child.stdin.write(`${JSON.stringify({ id: "commands", type: "get_commands" })}\n`);
      const { data } = await records.next((record) => record.id === "commands");
      return data.commands.find((command) => command.name === "skill:gripi");
    } finally {
      child.kill("SIGTERM");
      await new Promise((resolve) => child.once("exit", resolve));
    }
  };

  try {
    assert.match((await gripiSkill())?.description, /^Works with Gripi sessions through the gripi command\./);

    const own = path.join(agent, "skills", "gripi");
    await mkdir(own, { recursive: true });
    await writeFile(path.join(own, "SKILL.md"), "---\nname: gripi\ndescription: My own notes on Gripi.\n---\n");
    assert.equal((await gripiSkill())?.description, "My own notes on Gripi.");
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

// A gripi command that prints its arguments, or fails as gripi does when it can't reach the gateway.
async function fakeGripi(directory) {
  const command = path.join(directory, "gripi");
  await writeFile(command, `#!/usr/bin/env node
if (process.argv.includes("unreachable")) {
  process.stderr.write("gripi search: the gateway cannot be reached\\n");
  process.exit(1);
}
console.log(JSON.stringify({ args: process.argv.slice(2), token: process.env.GRIPI_USER_TOKEN }));
`, { mode: 0o755 });
  return command;
}

test("a Pi that finds sessions for Gripi searches and reads them with gripi, within the sidebar's filters", async (t) => {
  const directory = await mkdtemp(path.join(tmpdir(), "gripi-find-tools-"));
  const variables = { GRIPI_FIND_SESSIONS: await fakeGripi(directory), GRIPI_FIND_CWD: "/home/me/project", GRIPI_FIND_TAG: "bug", GRIPI_USER_TOKEN: "piu_alice" };
  const previous = Object.fromEntries(Object.keys(variables).map((name) => [name, process.env[name]]));
  t.after(async () => {
    for (const [name, value] of Object.entries(previous)) {
      if (value === undefined) delete process.env[name];
      else process.env[name] = value;
    }
    await rm(directory, { recursive: true, force: true });
  });

  assert.deepEqual([...(await loadTreeExtension()).tools.keys()], []);
  Object.assign(process.env, variables);
  const { tools } = await loadTreeExtension();
  assert.deepEqual([...tools.keys()], ["search_sessions", "show_session"]);
  const text = async (name, params) => JSON.parse((await tools.get(name).execute("call", params, undefined)).content[0].text);

  assert.deepEqual(await text("search_sessions", { text: "--all backup" }), { args: ["search", "--cwd", "/home/me/project", "--tag", "bug", "--", "--all backup"], token: "piu_alice" });
  assert.deepEqual(await text("show_session", { id: "01a2-b3" }), { args: ["show", "--all", "--", "01a2-b3"], token: "piu_alice" });
  await assert.rejects(tools.get("search_sessions").execute("call", { text: "unreachable" }, undefined), { message: "gripi search: the gateway cannot be reached" });
});

test("a Pi that finds sessions for Gripi has no other tools", { timeout: 30_000, skip: scriptedModelSkip }, async () => {
  const directory = await mkdtemp(path.join(tmpdir(), "gripi-find-pi-"));
  // The flags that Gripi starts such a Pi with (internal/agent/pi/adapter.go).
  const child = spawn(piExecutable, [
    "--mode", "rpc", "--no-session", "-ne", "--approve", "--extension", providerPath, "--extension", extensionPath, "--model", "fake/fake-1",
    "--tools", "search_sessions,show_session", "--exclude-tools", "mcp__*", "--no-skills", "--no-context-files", "--thinking", "low",
  ], {
    cwd: directory,
    env: { ...process.env, PI_CODING_AGENT_DIR: path.join(directory, "agent"), PI_OFFLINE: "1", PI_SKIP_VERSION_CHECK: "1", PI_TELEMETRY: "0", GRIPI_FIND_SESSIONS: await fakeGripi(directory) },
    stdio: ["pipe", "pipe", "pipe"],
  });
  const records = rpcRecords(child);
  try {
    const calls = [[{ name: "bash", arguments: { command: "touch escaped" } }, { name: "search_sessions", arguments: { text: "backup" } }]];
    child.stdin.write(`${JSON.stringify({ id: "run", type: "prompt", message: `RUN ${JSON.stringify(calls)}` })}\n`);
    const reply = await records.next((record) => record.type === "message_end" && record.message?.role === "assistant" && record.message.stopReason !== "toolUse");
    const [bash, search] = JSON.parse(reply.message.content[0].text);

    assert.equal(bash.isError, true);
    assert.equal(existsSync(path.join(directory, "escaped")), false);
    assert.equal(search.isError, false);
    assert.deepEqual(JSON.parse(search.text).args, ["search", "--", "backup"]);
  } finally {
    child.kill("SIGTERM");
    await new Promise((resolve) => child.once("exit", resolve));
    await rm(directory, { recursive: true, force: true });
  }
});

function rpcRecords(child) {
  let buffer = "";
  let stderr = "";
  const queued = [];
  const waiting = [];
  child.stderr.on("data", (chunk) => { stderr += chunk.toString(); });
  child.stdout.on("data", (chunk) => {
    buffer += chunk.toString();
    while (buffer.includes("\n")) {
      const newline = buffer.indexOf("\n");
      const line = buffer.slice(0, newline).replace(/\r$/, "");
      buffer = buffer.slice(newline + 1);
      if (!line) continue;
      const record = JSON.parse(line);
      const index = waiting.findIndex(({ predicate }) => predicate(record));
      if (index < 0) queued.push(record);
      else waiting.splice(index, 1)[0].resolve(record);
    }
  });
  child.on("exit", () => {
    for (const waiter of waiting.splice(0)) waiter.reject(new Error(`Pi RPC exited before responding: ${stderr}`));
  });

  return {
    next(predicate) {
      const index = queued.findIndex(predicate);
      if (index >= 0) return Promise.resolve(queued.splice(index, 1)[0]);
      return new Promise((resolve, reject) => waiting.push({ predicate, resolve, reject }));
    },
  };
}

test("large native trees are compacted before crossing the extension bridge", async () => {
  const directory = await mkdtemp(path.join(tmpdir(), "gripi-tree-extension-"));
  try {
    const jiti = createJiti(import.meta.url, {
      interopDefault: true,
      alias: { "@earendil-works/pi-coding-agent": piIndex },
    });
    const loadExtension = await jiti.import(extensionPath, { default: true });
    const commands = new Map();
    const events = new Map();
    loadExtension({
      registerCommand(name, command) { commands.set(name, command); },
      on(name, handler) { events.set(name, handler); },
      setLabel() {},
    });
    await events.get("session_start")({}, { cwd: directory, isProjectTrusted: () => true });

    const nodes = Array.from({ length: 1001 }, (_, index) => {
      const role = index === 1 ? "toolResult" : "user";
      const text = index === 0
        ? "x".repeat(20_000)
        : role === "toolResult" ? `Tool preview ${"y".repeat(20_000)}` : `Prompt ${index}`;
      return {
        entry: {
          id: `entry-${index}`,
          parentId: null,
          type: "message",
          timestamp: `2026-06-13T10:00:00Z${"t".repeat(2_000)}`,
          message: {
            role,
            content: [{ type: "text", text }, ...(index === 0 ? [{ type: "image", data: "RAW_IMAGE_DATA", mimeType: "image/png" }] : [])],
          },
        },
        children: [],
        label: index === 0 ? "checkpoint" : "l".repeat(2_000),
        labelTimestamp: "2026-06-13T10:00:00Z",
      };
    });

    let statusText;
    await commands.get("gripi_tree_snapshot").handler(
      `abc ${Buffer.from(JSON.stringify({ filter: "all" })).toString("base64url")}`,
      {
        sessionManager: { getTree: () => nodes, getLeafId: () => "entry-8" },
        ui: { setStatus(_key, value) { statusText = value; } },
      },
    );

    const snapshot = JSON.parse(statusText);
    assert.equal(snapshot.ok, true, snapshot.error);
    assert.ok(snapshot.entries.length < 1000);
    assert.equal(snapshot.truncated, true);
    assert.equal(snapshot.totalEntries, 1001);
    assert.equal(snapshot.leafId, "entry-8");
    assert.deepEqual(snapshot.entries.filter(({ current }) => current).map(({ entryId }) => entryId), ["entry-8"]);
    assert.deepEqual(snapshot.entries.filter(({ latest }) => latest).map(({ entryId }) => entryId), ["entry-1000"]);
    const first = snapshot.entries.find(({ entryId }) => entryId === "entry-0");
    const tool = snapshot.entries.find(({ entryId }) => entryId === "entry-1");
    assert.equal(first.label, "checkpoint");
    assert.ok(Buffer.byteLength(first.text, "utf8") <= 512);
    assert.match(tool.text, /^Tool preview/);
    assert.ok(Buffer.byteLength(tool.text, "utf8") <= 512);
    assert.ok(!statusText.includes("RAW_IMAGE_DATA"));
    assert.ok(Buffer.byteLength(statusText, "utf8") < 1_000_000);

    let reloadCalls = 0;
    const reloadArgs = (requestId) => `${requestId} ${Buffer.from("{}").toString("base64url")}`;
    await commands.get("gripi_reload").handler(reloadArgs("def"), {
      isIdle: () => true,
      reload: async () => { reloadCalls += 1; },
      ui: { setStatus(_key, value) { statusText = value; } },
    });
    assert.equal(reloadCalls, 1);
    assert.deepEqual(JSON.parse(statusText), { ok: true });

    await commands.get("gripi_reload").handler(reloadArgs("fed"), {
      isIdle: () => false,
      reload: async () => { reloadCalls += 1; },
      ui: { setStatus(_key, value) { statusText = value; } },
    });
    assert.equal(reloadCalls, 1);
    assert.deepEqual(JSON.parse(statusText), { ok: false, error: "Session is busy" });

    await commands.get("gripi_scoped_models").handler(reloadArgs("cab"), {
      scopedModels: [{ model: { provider: "openai-codex", id: "gpt-6-astra", name: "GPT-6 Astra" }, thinkingLevel: "high" }],
      ui: { setStatus(_key, value) { statusText = value; } },
    });
    assert.deepEqual(JSON.parse(statusText), { ok: true, models: [{ provider: "openai-codex", id: "gpt-6-astra" }] });
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

const MiB = 1024 * 1024;
const pngData = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==";

test("history bridge reads Pi's in-memory session in RPC mode", { timeout: 20_000 }, async () => {
  const directory = await mkdtemp(path.join(tmpdir(), "gripi-history-extension-"));
  const project = path.join(directory, "project");
  await mkdir(project);
  const child = spawn(piExecutable, [
    "--mode", "rpc", "--no-session", "-ne", "--approve",
    "--extension", path.resolve("test/helpers/fake_provider.ts"), "--extension", extensionPath, "--model", "fake/fake-1",
  ], {
    cwd: project,
    env: { ...process.env, PI_CODING_AGENT_DIR: path.join(directory, "agent"), PI_OFFLINE: "1", PI_SKIP_VERSION_CHECK: "1", PI_TELEMETRY: "0" },
    stdio: ["pipe", "pipe", "pipe"],
  });
  const records = rpcRecords(child);
  const bridgeRequest = async (name, requestId, payload) => {
    child.stdin.write(`${JSON.stringify({ id: requestId, type: "prompt", message: `/${name} ${requestId} ${Buffer.from(JSON.stringify(payload)).toString("base64url")}` })}\n`);
    const [response, status] = await Promise.all([
      records.next((record) => record.id === requestId),
      records.next((record) => record.statusKey === `${name}:${requestId}`),
    ]);
    assert.equal(response.success, true, response.error);
    return JSON.parse(status.statusText);
  };

  try {
    const script = JSON.stringify([[{ name: "bash", arguments: { command: "echo hi" } }]]);
    child.stdin.write(`${JSON.stringify({ id: "run", type: "prompt", message: `RUN ${script}`, images: [{ type: "image", data: pngData, mimeType: "image/png" }] })}\n`);
    await records.next((record) => record.type === "agent_settled");

    const history = await bridgeRequest("gripi_history", "a1", {});
    assert.equal(history.ok, true, history.error);
    const messages = history.entries.filter((entry) => entry.type === "message" && entry.message.role !== "system");
    assert.deepEqual(messages.map((entry) => entry.message.role), ["user", "assistant", "toolResult", "assistant"]);
    const prompt = messages[0];
    assert.deepEqual(prompt.message.content[1], { type: "image", data: "-", mimeType: "image/png" });

    assert.deepEqual(await bridgeRequest("gripi_history_image", "a2", { entryId: prompt.id, index: 0 }), { ok: true, data: pngData, mimeType: "image/png" });
  } finally {
    child.kill("SIGTERM");
    await new Promise((resolve) => child.once("exit", resolve));
    await rm(directory, { recursive: true, force: true });
  }
});

test("history replaces image data without changing Pi's entries, and reads one image by its number", async () => {
  const commands = await loadTreeExtension();
  const image = (data, mimeType = "image/png") => ({ type: "image", data, mimeType });
  const branch = branchOf(
    { role: "user", content: [{ type: "text", text: "Look" }, image("A"), image("B", "image/svg+xml"), image(""), image("C", "image/jpeg")] },
    { type: "custom_message", customType: "note", display: true, content: [image("D")] },
    { role: "toolResult", toolCallId: "r", toolName: "bash", content: [], details: { preview: image("E") }, isError: false },
  );
  const original = structuredClone(branch);
  const session = sessionOf(branch);

  const history = await bridge(commands, "gripi_history", {}, session);
  assert.deepEqual(history.entries[0].message.content.slice(1), [image("-"), image("-", "image/svg+xml"), image(""), image("-", "image/jpeg")]);
  assert.deepEqual(history.entries[1].content, [image("-")]);
  assert.deepEqual(history.entries[2].message.details, { preview: image("-") });
  assert.deepEqual(branch, original);

  const imageAt = (entryId, index) => bridge(commands, "gripi_history_image", { entryId, index }, session);
  assert.deepEqual(await imageAt("e0", 0), { ok: true, data: "A", mimeType: "image/png" });
  assert.deepEqual(await imageAt("e0", 1), { ok: true, data: "C", mimeType: "image/jpeg" });
  assert.deepEqual(await imageAt("e1", 0), { ok: true, data: "D", mimeType: "image/png" });
  assert.deepEqual(await imageAt("e0", 2), { ok: false, error: "Image not found" });
  assert.deepEqual(await imageAt("e2", 0), { ok: false, error: "Image not found" });
  assert.deepEqual(await imageAt("missing", 0), { ok: false, error: "Image not found" });
});

test("history image refuses images over 48 MiB", async () => {
  const commands = await loadTreeExtension();
  const branch = branchOf({ role: "user", content: [{ type: "image", data: "A".repeat(48 * MiB + 4), mimeType: "image/png" }] });

  assert.deepEqual(await bridge(commands, "gripi_history_image", { entryId: "e0", index: 0 }, sessionOf(branch)), { ok: false, error: "Image is too large" });
});

test("history keeps its reply small enough for Pi's output", async () => {
  const commands = await loadTreeExtension();
  const text = (size) => ({ role: "assistant", content: [{ type: "text", text: "x".repeat(size) }], stopReason: "stop" });
  const many = { role: "assistant", content: Array.from({ length: 6_000 }, () => ({ type: "text", text: "y".repeat(2_000) })), stopReason: "stop" };
  const branch = branchOf(text(10), ...Array.from({ length: 18 }, () => text(900 * 1024)), text(2 * MiB), many);
  const original = structuredClone(branch);

  const history = await bridge(commands, "gripi_history", {}, sessionOf(branch));
  // The newest entries that fit come back, so the oldest are left out.
  const ids = history.entries.map((entry) => Number(entry.id.slice(1)));
  assert.ok(ids[0] > 0);
  assert.deepEqual(ids, Array.from({ length: ids.length }, (_, index) => ids[0] + index));
  assert.equal(ids.at(-1), 20);
  assert.ok(JSON.stringify(history.entries).length <= 16 * MiB);
  assert.equal(history.entries.at(-2).message.content[0].text, "Message too large to show (2.0MB)");
  // An entry that is still too large keeps only its short strings.
  const cut = history.entries.at(-1).message;
  assert.equal(cut.role, "assistant");
  assert.match(cut.content[0].text, /^Message too large to show \(11\.\dMB\)$/);
  assert.deepEqual(branch, original);
});

async function loadTreeExtension() {
  const jiti = createJiti(import.meta.url, { interopDefault: true, alias: { "@earendil-works/pi-coding-agent": piIndex } });
  const loadExtension = await jiti.import(extensionPath, { default: true });
  const commands = new Map();
  const tools = new Map();
  loadExtension({ registerCommand(name, command) { commands.set(name, command); }, registerTool(tool) { tools.set(tool.name, tool); }, on() {}, setLabel() {} });
  return Object.assign(commands, { tools });
}

// Runs a bridge command as Gripi sends it and returns the extension's reply.
async function bridge(commands, name, payload, sessionManager) {
  let reply;
  await commands.get(name).handler(`abc ${Buffer.from(JSON.stringify(payload)).toString("base64url")}`, {
    sessionManager,
    ui: { setStatus(_key, value) { reply = value; } },
  });
  return JSON.parse(reply);
}

function sessionOf(branch) {
  return {
    getBranch: () => branch,
    getLeafId: () => branch.at(-1)?.id ?? null,
    getEntry: (id) => branch.find((entry) => entry.id === id),
  };
}

// Messages, or whole entries that have a type, become a chain of entries e0, e1, ….
function branchOf(...items) {
  return items.map((item, index) => ({
    type: "message",
    id: `e${index}`,
    parentId: index ? `e${index - 1}` : null,
    timestamp: "2026-11-23T10:00:00.000Z",
    ...(item.type ? item : { message: item }),
  }));
}
