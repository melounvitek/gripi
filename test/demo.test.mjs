import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import { test } from "node:test";

const html = await readFile(new URL("../demo/index.html", import.meta.url), "utf8");
const javascript = await readFile(new URL("../demo/demo.js", import.meta.url), "utf8");
const productionCSS = await readFile(new URL("../public/assets/app.css", import.meta.url), "utf8");
await import("../demo/demo.js");
const demo = globalThis.GripiDemo;

test("demo remains self-contained and embeds the production stylesheet", async () => {
  assert.deepEqual((await readdir(new URL("../demo/", import.meta.url))).sort(), ["demo.js", "index.html"]);
  assert.equal(html.match(/<style data-production-styles>\n(.*?)<\/style>/s)?.[1], productionCSS);
  assert.match(html, /<script src="demo\.js"><\/script>/);
  assert.doesNotMatch(html, /<(?:link|script|img|iframe|source|object|embed)[^>]+(?:href|src|data)=["'](?:https?:|\/)/i);
  assert.doesNotMatch(javascript, /\b(?:fetch|XMLHttpRequest|EventSource|WebSocket|sendBeacon)\b/);
  assert.doesNotMatch(javascript, /^\s*(?:import|export)\s/m);
});

test("demo exposes the guide catalogue and safe portable helpers", () => {
  assert.equal(demo.defaultSessionId, "welcome");
  assert.ok(demo.demoSessionCount >= 8);
  assert.equal(demo.hasUnreadSessions, false);
  assert.deepEqual(demo.safeGuideLink({ href: "https://pi.dev/", label: "Pi" }), { href: "https://pi.dev/", label: "Pi" });
  assert.equal(demo.safeGuideLink({ href: "javascript:alert(1)", label: "Unsafe" }), null);
  assert.equal(demo.safeIdentityColor("#12abEF", "#000000"), "#12abEF");
  assert.equal(demo.safeIdentityColor("red;background:url(//example.test)", "#123456"), "#123456");
  assert.equal(demo.formatDemoTimestamp(new Date(2026, 6, 17, 16, 36)), "2026-07-17 16:36");
});

test("demo scripted responses finish, cancel, and include visible stages", async () => {
  const events = [];
  assert.equal(await demo.playScript(
    [{ type: "status" }, { type: "delta", text: "Hello" }, { type: "done" }],
    { wait: async () => {}, onEvent: ({ type }) => events.push(type) },
  ), true);
  assert.deepEqual(events, ["status", "delta", "done"]);

  const controller = new AbortController();
  const cancelled = [];
  assert.equal(await demo.playScript(
    [{ type: "delta", text: "A" }, { type: "delta", text: "B" }],
    { signal: controller.signal, wait: async () => controller.abort(), onEvent: ({ text }) => cancelled.push(text) },
  ), false);
  assert.deepEqual(cancelled, []);

  const response = demo.responseScript("How does this work?");
  assert.deepEqual([...new Set(response.map(({ type }) => type))], ["status", "thinking", "tool_start", "tool_end", "assistant_start", "delta", "done"]);
  assert.match(response.filter(({ type }) => type === "delta").map(({ text }) => text).join(""), /Pi coding-agent harness/);
});

test("demo compact tool and inline-code markup follows production semantics", () => {
  assert.deepEqual(demo.inlineCodeParts("Use `go.mod`, not `vendor/`."), [
    { type: "text", text: "Use " },
    { type: "code", text: "go.mod" },
    { type: "text", text: ", not " },
    { type: "code", text: "vendor/" },
    { type: "text", text: "." },
  ]);
  assert.deepEqual(demo.toolSummaryParts("bash git diff --check"), [{ type: "text", text: "$ git diff --check" }]);
  assert.deepEqual(demo.toolSummaryParts("read app/components/sidebar/search.tsx"), [
    { type: "command", text: "read" },
    { type: "path", text: "app/components/sidebar/search.tsx" },
  ]);
  assert.match(javascript, /role === "tool" \? " message--compact message--tool-call"/);
  assert.doesNotMatch(javascript, /dataToolOutputBody|dataToolOutputToggle/);
});

test("demo preserves first-touch controls and accessible static UI contracts", () => {
  for (const expected of [
    'function openSelectOnFirstTouch(trigger, closed, open) {',
    'trigger.addEventListener("touchmove", trackTouch);',
    'openSelectOnFirstTouch(element.projectTrigger, () => element.projectList.hidden, openProjectList);',
    'openSelectOnFirstTouch(element.viewTrigger, () => element.viewList.hidden, openConversationView);',
    'openSelectOnFirstTouch(newSessionTrigger, () => newSessionList.hidden, openNewSessionList);',
    'const introSeenKey = "gripi:static-demo:intro-seen";',
    'if (!introSeen()) openModal("demo-intro-modal", null);',
  ]) assert.ok(javascript.includes(expected), `missing ${expected}`);

  for (const expected of [
    'role="dialog" aria-modal="true" aria-labelledby="demo-intro-title"',
    'data-modal-open="demo-intro-modal"',
    'data-conversation-view-trigger',
    'data-conversation-view-listbox',
    'placeholder="Ask Pi…"',
    'openai-codex/gpt-5.5 (medium)',
  ]) assert.ok(html.includes(expected), `missing ${expected}`);
  assert.match(html, /@media \(pointer: coarse\) \{[\s\S]*?\.send-button \{ display: inline-flex;/);
});
