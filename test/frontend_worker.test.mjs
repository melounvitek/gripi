import assert from "node:assert/strict";
import { test } from "node:test";

import { WorkerController } from "../public/assets/worker_controller.js";
import { FakeDocument, FakeElement, FakeEventTarget } from "./helpers/fake_dom.mjs";

const NOW = Date.parse("2026-10-31T18:44:31Z");
const SESSION = "/home/me/.pi/agent/sessions/--home-me-Work-storefront--/s.jsonl";
const at = (seconds) => new Date(NOW + seconds * 1000).toISOString();
const connected = { worker: "beelink", state: "connected", repository: "/home/me/Work/storefront", ports: [{ port: 5173, local: 5173 }] };

function reply(status, body = {}) {
  return { ok: status === 200, status, json: async () => body };
}

function text(element) {
  return element.children?.length ? element.children.map(text).join("") : element.textContent;
}

function fixture({ replies = [] } = {}) {
  const document = new FakeDocument();
  const part = (selector, tagName = "span") => new FakeElement(tagName, [selector]);
  const badge = part("[data-worker-badge]");
  const badgeName = part("[data-worker-badge-name]");
  const badgeState = part("[data-worker-badge-state]");
  badge.append(badgeName, badgeState);
  const prompt = part("[data-worker-prompt]", "section");
  const parts = Object.fromEntries(["title", "time", "progress", "facts", "actions", "separator", "hint"].map((name) => [name, part(`[data-worker-prompt-${name}]`)]));
  const proceed = part("[data-worker-continue]", "button");
  const wait = part("[data-worker-wait]", "button");
  prompt.append(...Object.values(parts), proceed, wait);
  const notice = part("[data-worker-notice]", "div");
  const noticeTitle = part("[data-worker-notice-title]");
  const noticeText = part("[data-worker-notice-text]");
  const dismiss = part("[data-worker-notice-dismiss]", "button");
  notice.append(noticeTitle, noticeText, dismiss);
  for (const element of [badge, prompt, notice]) element.hidden = true;
  const form = new FakeElement("form");
  const statusBar = new FakeElement("div");
  const window = new FakeEventTarget();
  const sent = [];
  window.fetch = async (url, options) => {
    sent.push({ url, body: Object.fromEntries(options.body) });
    return replies.shift() || reply(200, { ok: true });
  };
  const calls = { refresh: 0, stateChanged: 0, failed: [] };
  const controller = new WorkerController(document, window, {
    sessionPath: () => SESSION,
    canFocus: () => true,
    refresh: () => calls.refresh++,
    stateChanged: () => calls.stateChanged++,
    homePath: (path) => path.replace(/^\/home\/me/, "~"),
    failed: (message) => calls.failed.push(message)
  });
  controller.bind({ badge, prompt, form, statusBar, notice });
  const click = (element) => element.listeners.get("click").forEach((listener) => listener({}));
  const key = (name) => {
    const event = { key: name, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; } };
    prompt.listeners.get("keydown").forEach((listener) => listener(event));
    return event;
  };
  const facts = () => parts.facts.children.map((child) => child.textContent);
  return { controller, badge, badgeName, badgeState, prompt, parts, proceed, wait, notice, noticeTitle, noticeText, dismiss, form, statusBar, sent, calls, click, key, facts };
}

const flush = () => new Promise((resolve) => setImmediate(resolve));

test("a session on a connected worker shows it quietly in the header and in the editor's footer, like a Pi status", () => {
  const { controller, badge, badgeName, badgeState, prompt, form, statusBar, calls } = fixture();

  controller.update(connected, []);
  assert.equal(badge.hidden, false);
  assert.equal(badge.dataset.workerState, "connected");
  assert.equal(badge.title, "Runs on beelink · connected · main's copy of storefront is read-only");
  assert.equal(badgeName.textContent, "beelink");
  assert.equal(badgeState.textContent, "");
  assert.equal(text(statusBar), "beelink connected · port 5173");
  assert.equal(prompt.hidden, true);
  assert.equal(form.hasAttribute("data-worker"), false);
  assert.equal(calls.stateChanged, 0);

  controller.update({ ...connected, state: "disconnected", ports: [{ port: 3000, local: 3001 }, { port: 5173, local: 5173 }] }, []);
  assert.equal(text(statusBar), "beelink ports 3000→3001, 5173");
  // The sidebar's icons follow.
  assert.equal(calls.stateChanged, 1);

  controller.update(null, []);
  assert.equal(badge.hidden, true);
  assert.equal(statusBar.children.length, 0);
});

test("copying the repository takes the editor's place, with its stage and how long it has taken", (t) => {
  t.mock.timers.enable({ apis: ["setInterval", "Date"], now: NOW });
  const { controller, prompt, parts, form, badgeState, facts } = fixture();

  controller.update({ ...connected, state: "connecting", copying: at(-12), ports: [] }, []);
  assert.equal(prompt.hidden, false);
  assert.equal(prompt.dataset.mode, "copying");
  assert.equal(form.hasAttribute("data-worker"), true);
  assert.equal(badgeState.textContent, "copying");
  assert.equal(parts.title.textContent, "Copying storefront to beelink");
  assert.equal(parts.time.textContent, "0:12");
  assert.equal(parts.progress.hidden, false);
  assert.equal(parts.actions.hidden, true);
  assert.deepEqual(facts(), ["moves", "storefront, with its worktrees and sessions", "now", "connecting to beelink, and copying Pi there the first time", "on main", "storefront stays read-only while it runs there"]);
  t.mock.timers.tick(61_000);
  assert.equal(parts.time.textContent, "1:13");

  controller.update(connected, []);
  assert.equal(prompt.hidden, true);
  assert.equal(form.hasAttribute("data-worker"), false);
});

test("a lost worker counts down to when it must have stopped, then a single tap or Enter continues on main", async (t) => {
  t.mock.timers.enable({ apis: ["setInterval", "Date"], now: NOW });
  const { controller, prompt, parts, proceed, wait, sent, calls, click, facts } = fixture();

  controller.update({ ...connected, state: "unreachable", gone_at: at(24), ports: [] }, []);
  assert.equal(prompt.dataset.mode, "unreachable");
  assert.equal(parts.title.textContent, "beelink is not answering");
  assert.equal(proceed.disabled, true);
  assert.equal(text(proceed), "continue on main in 0:24");
  assert.equal(wait.hidden, true);
  assert.equal(facts()[0], "steps");
  t.mock.timers.tick(10_000);
  assert.equal(text(proceed), "continue on main in 0:14");

  // Gone before the next poll says so.
  t.mock.timers.tick(14_000);
  assert.equal(prompt.dataset.mode, "gone");
  assert.equal(parts.title.textContent, "beelink is gone");
  assert.equal(proceed.disabled, false);
  assert.equal(text(proceed), "continue on main");
  // Enter presses the focused button, as it would send what the editor holds.
  assert.equal(proceed.focused, true);
  assert.equal(wait.hidden, false);
  assert.equal(wait.textContent, "keep waiting");

  click(proceed);
  assert.equal(prompt.dataset.mode, "returning");
  assert.equal(parts.title.textContent, "Bringing storefront back from beelink");
  await flush();
  assert.deepEqual(sent, [{ url: "/workers/return", body: { session: SESSION } }]);
  assert.equal(calls.refresh, 2);
  controller.update(null, []);
  assert.equal(prompt.hidden, true);
});

test("keep waiting puts a gone worker's block away until the worker is lost again", (t) => {
  t.mock.timers.enable({ apis: ["setInterval", "Date"], now: NOW });
  const { controller, prompt, form, statusBar, key } = fixture();
  const gone = { ...connected, state: "gone", gone_at: at(-5), ports: [] };

  controller.update(gone, []);
  assert.equal(key("Escape").defaultPrevented, true);
  assert.equal(prompt.hidden, true);
  assert.equal(form.hasAttribute("data-worker"), false);
  assert.equal(text(statusBar), "beelink gone");
  controller.update(gone, []);
  assert.equal(prompt.hidden, true);

  controller.update({ ...gone, state: "unreachable", gone_at: at(60) }, []);
  assert.equal(prompt.dataset.mode, "unreachable");
});

test("a refused move shows the gateway's reason in the editor's place until it is seen", async (t) => {
  t.mock.timers.enable({ apis: ["setInterval", "Date"], now: NOW });
  const reason = "session s.jsonl is working; wait until it finishes or stop it";
  const { controller, prompt, parts, proceed, wait, sent, key, facts } = fixture({ replies: [reply(409, { error: reason })] });

  const moved = controller.move("beelink", "/home/me/Work/storefront");
  assert.equal(prompt.dataset.mode, "copying");
  assert.equal(parts.title.textContent, "Copying storefront to beelink");
  assert.equal(facts()[3], "stopping the repository's sessions here");
  assert.equal(await moved, false);
  assert.deepEqual(sent, [{ url: "/workers/move", body: { session: SESSION, worker: "beelink" } }]);
  assert.equal(prompt.dataset.mode, "failed");
  assert.equal(parts.title.textContent, "Could not move storefront");
  assert.deepEqual(facts(), ["error", reason]);
  assert.equal(proceed.hidden, true);
  assert.equal(wait.textContent, "ok");
  assert.equal(wait.focused, true);

  key("Escape");
  assert.equal(prompt.hidden, true);
});

test("main's files saved aside show in the sync banner, naming the folder, until dismissed", async () => {
  const { controller, notice, noticeTitle, noticeText, dismiss, sent, click } = fixture();
  const notices = [
    { repository: "/home/me/Work/storefront", aside: "/home/me/.pi/gripi/mirrors/5f1c/aside/1/main/6", paths: ["/home/me/Work/storefront/src/cart/totals.ts"] },
    { repository: "/home/me/Work/storefront", aside: "/home/me/.pi/gripi/mirrors/5f1c/aside/1/main/7", paths: ["/home/me/Work/storefront/.env.local"] }
  ];

  controller.update(connected, notices);
  assert.equal(notice.hidden, false);
  assert.equal(noticeTitle.textContent, "2 files changed on main while storefront ran on beelink.");
  assert.equal(noticeText.textContent, "beelink's version replaced them. Main's version of src/cart/totals.ts and .env.local is saved in ~/.pi/gripi/mirrors/5f1c/aside/1/main/7 and 1 earlier folder beside it, at their paths.");

  click(dismiss);
  await flush();
  assert.deepEqual(sent, [{ url: "/workers/dismiss_notices", body: { session: SESSION } }]);
  assert.equal(notice.hidden, true);
});
