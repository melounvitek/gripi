import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtemp, realpath, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { test } from "node:test";

const piExecutable = process.env.GRIPI_PI || execFileSync("sh", ["-c", "command -v pi"], { encoding: "utf8" }).trim();
const piPackageRoot = path.resolve(path.dirname(await realpath(piExecutable)), "..");
const { createJiti } = await import(pathToFileURL(path.join(piPackageRoot, "node_modules/jiti/lib/jiti.mjs")));
const piIndex = path.join(piPackageRoot, "dist/index.js");
const extensionPath = path.resolve("pi_extensions/gripi-tree.ts");

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

    const assistantCases = {
      2: { content: "Final answer", stopReason: "stop" },
      3: { content: "Working", stopReason: "toolUse" },
      4: { content: "Long answer", stopReason: "length" },
      5: { content: "Legacy answer" },
      6: { content: "Partial answer", stopReason: "aborted" },
      7: { content: "Failed answer", stopReason: "error" },
      8: { content: "  ", stopReason: "stop" },
      9: { content: "Commentary", stopReason: "stop", commentary: true },
    };
    const nodes = Array.from({ length: 1001 }, (_, index) => {
      const assistant = assistantCases[index];
      const role = index === 1 ? "toolResult" : assistant ? "assistant" : "user";
      const text = index === 0
        ? "x".repeat(20_000)
        : role === "toolResult" ? `Tool preview ${"y".repeat(20_000)}` : assistant?.content || `Prompt ${index}`;
      return {
        entry: {
          id: `entry-${index}`,
          parentId: null,
          type: "message",
          timestamp: `2026-06-13T10:00:00Z${"t".repeat(2_000)}`,
          message: {
            role,
            content: [{
              type: "text",
              text,
              ...(assistant?.commentary ? { textSignature: JSON.stringify({ v: 1, id: "message-1", phase: "commentary" }) } : {}),
            }, ...(index === 0 ? [{ type: "image", data: "RAW_IMAGE_DATA", mimeType: "image/png" }] : [])],
            ...(assistant?.stopReason ? { stopReason: assistant.stopReason } : {}),
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
    assert.deepEqual(
      snapshot.entries.filter(({ entryId, messageKind }) => /^entry-[2-9]$/.test(entryId) && messageKind === "assistant-final").map(({ entryId }) => entryId).sort(),
      ["entry-2", "entry-4", "entry-5"],
    );
    assert.ok(!statusText.includes("RAW_IMAGE_DATA"));
    assert.ok(Buffer.byteLength(statusText, "utf8") < 1_000_000);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});
