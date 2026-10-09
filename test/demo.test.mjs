import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import { test } from "node:test";
import { githubBlock } from "../public/assets/environment_controller.js";

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
  const now = new Date(2026, 6, 17, 20, 0);
  assert.equal(demo.formatDemoTimestamp(new Date(2026, 6, 17, 16, 36), now), "16:36");
  assert.equal(demo.formatDemoTimestamp(new Date(2026, 6, 16, 9, 5), now), "Jul 16 09:05");
  assert.equal(demo.formatDemoTimestamp(new Date(2025, 6, 17, 16, 36), now), "Jul 17 2025 16:36");
});

test("every demo session includes activity that distinguishes the transcript views", () => {
  for (const session of demo.sessionCatalog) {
    assert.equal(session.hasActivity, true, `${session.name} has no thinking or tool activity`);
  }

  const normalized = demo.normalizeSession({
    id: "local-session",
    messages: [
      { role: "user", text: "Help me with this project." },
      { role: "assistant", text: "What would you like to change?" },
    ],
  });
  assert.deepEqual(normalized.messages.map(({ role }) => role), ["user", "thinking", "assistant"]);

  const restored = demo.normalizeSession({
    id: "restored-session",
    messages: Array.from({ length: 4 }, (_, index) => ({ role: "user", text: `Message ${index + 1}` })),
  });
  assert.equal(restored.messages.at(-1).role, "thinking");
  assert.match(javascript, /sessions\.unshift\(normalizeSession\(\{ \.\.\.source, id, name: `\$\{source\.name\} \(fork\)`[\s\S]*?messages: source\.messages\.slice\(0, 4\)/);
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
  assert.deepEqual([...new Set(response.map(({ type }) => type))], ["status", "thinking", "tool_start", "tool_end", "paragraph", "done"]);
  const paragraphs = response.filter(({ type }) => type === "paragraph");
  assert.match(paragraphs.map(({ text }) => text).join("\n\n"), /^This is a prerecorded response to “How does this work\?”\. .*Pi coding-agent harness.*\n\nThe static demo still mirrors .*while it is streaming\.$/s);
  assert.equal(response.some(({ text }) => text?.includes("\n\n")), false);
  // Each paragraph lands whole once its words would have streamed.
  for (const { text, delay } of paragraphs) assert.ok(delay >= 42 * text.split(/\s+/).length, text);
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

test("demo has the production Environment key and dialog and keeps saved values out of storage", () => {
  for (const expected of [
    // The key follows the bell.
    '</svg></button><button type="button" class="sidebar-tool" title="Environment variables" aria-label="Environment variables" data-modal-open="environment-modal">',
    '<div class="modal-overlay" data-modal="environment-modal" hidden><div class="modal-card picker-card environment-card" role="dialog" aria-modal="true" aria-labelledby="environment-modal-title">',
    "data-environment-description>Pi and every command it runs get these variables in every Gripi session, on top of the gateway's own environment.</p>",
    '<span class="picker-hint">enter save · esc cancel</span>',
    "data-environment-github>GitHub: act as yourself</p>",
    "data-environment-github>Fill in the empty values and save. Lines starting with # are ignored. Create the token at github.com/settings/tokens; a classic token needs the repo, read:org and gist scopes.</p>",
    "data-environment-pasted>One NAME=value per line. Lines starting with # are ignored.</p>",
    '<span class="picker-hint">ctrl+enter save · esc cancel</span>',
    "data-environment-warning hidden>GH_TOKEN covers gh only. Commits and git push still use the gateway's identity.</p>",
    "data-environment-keys>↑↓ navigate · enter open · esc close</p>",
  ]) assert.ok(html.includes(expected), `missing ${expected}`);

  // A visitor may paste a real token. Everything the demo stores: that the intro was seen, its sessions, the hidden sidebar and the composer's draft.
  assert.doesNotMatch(javascript, /\b(?:sessionStorage|cookie|indexedDB)\b/);
  assert.deepEqual(javascript.match(/localStorage\.setItem\(.*?\);/g), [
    'localStorage.setItem(introSeenKey, "true");',
    'localStorage.setItem(storageKey, JSON.stringify({ sessions, currentId }));',
    'localStorage.setItem(desktopSidebarHiddenKey, "true");',
    'localStorage.setItem(draftKey(id), element.prompt.value);',
  ]);
});

test("demo reads a block of variables by the gateway's rules and offers the GitHub lines of the real dialog", () => {
  assert.deepEqual(demo.parseEnvironmentBlock("# a note\n FAKE_ONE = fake one \n\nFAKE_TWO=\"fake=two\"\n"), { variables: [["FAKE_ONE", "fake one"], ["FAKE_TWO", "fake=two"]] });
  // The first line the gateway would reject stops the block, in the gateway's words.
  for (const [text, error] of [
    ["FAKE_ONE=fake\nnot a pair", "Line 2: expected NAME=value."],
    ["FAKE_ONE=fake\n\nFAKE_ONE=again", "Line 3: FAKE_ONE is set twice."],
    ["FAKE ONE=fake", "Line 1: “FAKE ONE” is not a valid name. Use letters, digits and _."],
    ["HOME=/fake", "Line 1: “HOME” is reserved for Gripi and Pi."],
    ["GRIPI_PORT=1", "Line 1: “GRIPI_PORT” is reserved for Gripi and Pi."],
    ["PI_CODING_AGENT_DIR=/fake", "Line 1: “PI_CODING_AGENT_DIR” is reserved for Gripi and Pi."],
    ["FAKE_ONE=''", "Line 1: FAKE_ONE has no value."],
  ]) assert.deepEqual(demo.parseEnvironmentBlock(text), { error });

  // All ten lines with nothing saved, then the missing ones, then none.
  const names = githubBlock([]).split("\n").filter((line) => !line.startsWith("#")).map((line) => line.split("=")[0]);
  assert.equal(names.length, 10);
  for (const saved of [[], ["GH_TOKEN", "GIT_CONFIG_COUNT"]]) assert.equal(demo.githubBlock(saved), githubBlock(saved));
  assert.equal(demo.githubBlock(names), "");
});

test("demo preserves first-touch controls and accessible static UI contracts", () => {
  for (const expected of [
    'function openSelectOnFirstTouch(trigger, closed, open) {',
    'trigger.addEventListener("touchmove", trackTouch);',
    'openSelectOnFirstTouch(element.projectTrigger, () => element.projectList.hidden, openProjectList);',
    'const introSeenKey = "gripi:static-demo:intro-seen";',
    'const desktopSidebarHiddenKey = "gripi:desktop-sidebar-hidden";',
    'sidebarVisibilityToggles: document.querySelectorAll("[data-sidebar-visibility-toggle]")',
    'element.sidebarVisibilityToggles.forEach((toggle) => {',
    'if (!introSeen()) openModal("demo-intro-modal", null);',
    'enabled ? "Demo notifications on — click to disable" : "Demo notifications off — click to enable"',
  ]) assert.ok(javascript.includes(expected), `missing ${expected}`);

  for (const expected of [
    'role="dialog" aria-modal="true" aria-labelledby="demo-intro-title"',
    'data-modal-open="demo-intro-modal"',
    'data-conversation-view-toggle',
    'role="group" aria-label="Agent activity"',
    'data-conversation-view="brief" aria-pressed="false"',
    'data-conversation-view="full" aria-pressed="true"',
    'class="desktop-sessions-button desktop-sessions-close-button"',
    'class="desktop-sessions-button desktop-sessions-open-button"',
    'data-sidebar-visibility-toggle',
    'title="Demo notifications off — click to enable" aria-label="Demo notifications off — click to enable" data-notification-toggle',
    'placeholder="Ask Pi…"',
    'openai-codex/gpt-5.5 (medium)',
  ]) assert.ok(html.includes(expected), `missing ${expected}`);
  assert.match(html, /@media \(pointer: coarse\) \{[\s\S]*?\.send-button \{ display: inline-flex;/);
  assert.match(javascript, /pin\.setAttribute\("aria-label", session\.pinned \? `Unpin session \$\{session\.name\}` : `Pin session \$\{session\.name\}`\)/);
});
