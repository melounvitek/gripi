#!/usr/bin/env node

// Retakes the README screenshots from the real gateway, with invented sessions and a fake Pi.
import { once } from "node:events";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { spawn, spawnSync } from "node:child_process";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "@playwright/test";
import { sessions } from "./readme_screenshot_sessions.mjs";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
// The sidebar shows this origin.
const port = 4599;
const baseURL = `http://localhost:${port}`;
const runtimeRoot = await mkdtemp(path.join(os.tmpdir(), "gripi-screenshots-"));
const home = path.join(runtimeRoot, "home");
const sessionsRoot = path.join(home, ".pi", "agent", "sessions");
const state = path.join(home, ".pi", "gripi");
let server;

try {
  const paths = await seed();
  // An up-to-date master checkout of its own, so the sidebar shows no update notice.
  const checkout = path.join(runtimeRoot, "checkout");
  const binary = path.join(checkout, "tmp", "gripi");
  await mkdir(checkout);
  await writeFile(path.join(checkout, ".gitignore"), "tmp/\n");
  for (const [command, ...args] of [
    ["git", "init", "--quiet", "--initial-branch=master"],
    ["git", "add", ".gitignore"],
    ["git", "-c", "user.name=Gripi", "-c", "user.email=gripi@localhost", "commit", "--quiet", "--message=Screenshots"],
    ["git", "remote", "add", "origin", checkout],
    ["go", "build", "-C", repoRoot, "-o", binary, "./cmd/gripi"]
  ]) {
    const result = spawnSync(command, args, { cwd: checkout, encoding: "utf8" });
    if (result.status !== 0) throw new Error(result.stderr || result.stdout || result.error?.message);
  }

  server = spawn(binary, ["serve"], {
    cwd: checkout,
    stdio: ["ignore", "ignore", "inherit"],
    env: {
      PATH: process.env.PATH,
      HOME: home,
      GRIPI_PORT: String(port),
      GRIPI_BROWSER_AUTH_DISABLED: "1",
      GRIPI_NODE: process.execPath,
      GRIPI_PI: path.join(repoRoot, "e2e", "support", "fake_pi.mjs")
    }
  });
  await waitForServer(`${baseURL}/apple-touch-icon.png`, server);
  const browser = await chromium.launch();
  try {
    await screenshot(browser, {
      file: "gripi-desktop-screenshot.png", session: paths.desktop, view: "full", topText: "$ npx vitest run src/cart",
      device: { viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 }
    });
    await screenshot(browser, {
      file: "gripi-mobile-screenshot.png", session: paths.mobile, view: "brief", topText: "The nightly restic backup",
      device: { viewport: { width: 402, height: 874 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true }
    });
  } finally {
    await browser.close();
  }
} finally {
  if (server && server.exitCode === null && server.signalCode === null) {
    server.kill();
    await once(server, "exit");
  }
  await rm(runtimeRoot, { recursive: true, force: true });
}

// The conversation starts at the message containing topText.
async function screenshot(browser, { file, session, view, topText, device }) {
  const context = await browser.newContext({ ...device, colorScheme: "dark" });
  await context.addInitScript((view) => {
    // Headless Chromium blocks notifications, which the sidebar would show in red.
    localStorage.setItem("gripi:notifications-disabled", "true");
    localStorage.setItem("gripi:conversation-view", view);
  }, view);
  const page = await context.newPage();
  await page.goto(`${baseURL}/?session=${encodeURIComponent(session)}`);
  await page.waitForTimeout(500);
  await page.evaluate((text) => {
    document.activeElement?.blur();
    const scroll = document.getElementById("conversation-scroll");
    const target = [...scroll.querySelectorAll(".message")].find((message) => message.textContent.includes(text));
    scroll.scrollTop += target.getBoundingClientRect().top - scroll.getBoundingClientRect().top - 12;
  }, topText);
  await page.screenshot({ path: path.join(repoRoot, "docs", "images", file) });
  console.log(`Wrote docs/images/${file}`);
  await context.close();
}

async function seed() {
  await Promise.all([state, sessionsRoot].map((directory) => mkdir(directory, { recursive: true })));
  const now = Date.now();
  const pinned = [];
  const tags = {};
  const readCounts = {};
  const result = {};
  for (const session of sessions) {
    const cwd = path.join(home, "Work", session.project);
    await mkdir(cwd, { recursive: true });
    const file = path.join(sessionsRoot, `${session.slug}.jsonl`);
    const entries = sessionEntries(session, cwd, now - session.minutesAgo * 60_000);
    await writeFile(file, `${entries.map((entry) => JSON.stringify(entry)).join("\n")}\n`);
    if (session.pinned) pinned.push(file);
    if (session.tags) tags[file] = session.tags;
    // Sessions missing from the read state start read.
    if (session.unread) readCounts[file] = 0;
    if (session.screenshot) result[session.screenshot] = file;
  }
  await writeFile(path.join(state, "pinned-sessions.json"), JSON.stringify(pinned));
  await writeFile(path.join(state, "session-tags.json"), JSON.stringify(tags));
  await writeFile(path.join(state, "read-state.json"), JSON.stringify(readCounts));
  return result;
}

// Pi's own session format: one entry per line, each pointing at the one before it.
function sessionEntries(session, cwd, endsAt) {
  const entryCount = 3 + session.turns.reduce((count, turn) => count + 1 + (turn.tools?.length || 0), 0);
  let clock = endsAt - entryCount * 20_000;
  let parentId = null;
  let tokens = 24_000;
  const entries = [{ type: "session", version: 3, id: `readme-${session.slug}`, timestamp: new Date(clock).toISOString(), cwd }];
  const add = (type, fields) => {
    clock += 20_000;
    const id = `${session.slug}-${entries.length}`;
    entries.push({ type, id, parentId, timestamp: new Date(clock).toISOString(), ...fields });
    parentId = id;
  };

  add("model_change", { provider: "anthropic", modelId: "claude-opus-4-5" });
  add("thinking_level_change", { thinkingLevel: "high" });
  for (const turn of session.turns) {
    if (turn.user) {
      add("message", { message: { role: "user", content: [{ type: "text", text: turn.user }], timestamp: clock } });
      continue;
    }
    const calls = (turn.tools || []).map((tool, index) => {
      const name = "bash" in tool ? "bash" : "read" in tool ? "read" : "edit";
      const args = name === "bash" ? { command: tool.bash } : { path: tool[name] };
      return { tool, name, args, id: `${parentId}-call-${index}` };
    });
    const content = [
      ...(turn.thinking ? [{ type: "thinking", thinking: turn.thinking }] : []),
      ...(turn.text ? [{ type: "text", text: turn.text }] : []),
      ...calls.map(({ id, name, args }) => ({ type: "toolCall", id, name, arguments: args }))
    ];
    tokens += 1_800;
    const usage = { input: 3, output: 400, cacheRead: tokens - 403, cacheWrite: 0, totalTokens: tokens, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } };
    add("message", { message: { role: "assistant", content, api: "anthropic-messages", provider: "anthropic", model: "claude-opus-4-5", usage, stopReason: calls.length ? "toolUse" : "stop", timestamp: clock } });
    for (const { tool, name, id } of calls) {
      const failed = Boolean(tool.exitCode);
      const text = name === "edit" ? `Successfully replaced 1 block(s) in ${tool.edit}.` : `${tool.output}${failed ? `\n\n\nCommand exited with code ${tool.exitCode}` : ""}`;
      add("message", { message: { role: "toolResult", toolCallId: id, toolName: name, content: [{ type: "text", text }], ...(name === "edit" ? { details: { diff: tool.diff } } : {}), isError: failed, timestamp: clock } });
    }
  }
  add("session_info", { name: session.title });
  return entries;
}

async function waitForServer(url, child) {
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    if (child.exitCode !== null) throw new Error(`Gateway exited before becoming ready (${child.exitCode})`);
    try {
      if ((await fetch(url)).status === 200) return;
    } catch (_error) {}
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`Gateway did not become ready at ${url}`);
}
