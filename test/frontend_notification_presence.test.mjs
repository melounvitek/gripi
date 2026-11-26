import assert from "node:assert/strict";
import { test } from "node:test";

import { NotificationPresenceController } from "../public/assets/notification_presence.js";
import { FakeElement } from "./helpers/fake_dom.mjs";

test("notification presence follows focus, session changes, heartbeats, and page exit", async () => {
  const requests = [];
  const document = eventTarget({ hidden: false, focused: true });
  document.hasFocus = () => document.focused;
  let interval;
  const window = eventTarget({
    setInterval(callback, milliseconds) {
      interval = { callback, milliseconds };
      return 1;
    }
  });
  let sessionPath = "/sessions/one.jsonl";
  const controller = new NotificationPresenceController(
    document,
    window,
    () => sessionPath,
    async (_url, options) => { requests.push({ ...options, body: JSON.parse(options.body) }); },
    "desktop-window"
  );

  controller.start();
  await flush();
  assert.deepEqual(requests.at(-1).body, {
    client_id: "desktop-window", session: "/sessions/one.jsonl", focused: true, sequence: 1
  });
  assert.equal(interval.milliseconds, 10_000);

  sessionPath = "/sessions/two.jsonl";
  controller.sessionChanged();
  await flush();
  assert.equal(requests.at(-1).body.session, "/sessions/two.jsonl");

  const heartbeatCount = requests.length;
  interval.callback();
  await flush();
  assert.equal(requests.length, heartbeatCount + 1);
  assert.equal(requests.at(-1).body.focused, true);

  document.hidden = true;
  document.dispatch("visibilitychange");
  await flush();
  assert.deepEqual(requests.at(-1).body, {
    client_id: "desktop-window", session: "", focused: false, sequence: 4
  });

  document.hidden = false;
  document.focused = true;
  window.dispatch("focus");
  await flush();
  assert.equal(requests.at(-1).body.session, "/sessions/two.jsonl");
  assert.equal(requests.at(-1).body.focused, true);

  window.dispatch("pagehide");
  await flush();
  assert.equal(requests.at(-1).body.focused, false);
  assert.equal(requests.at(-1).body.sequence, 6);
  assert.equal(requests.at(-1).keepalive, true);
});

test("notification presence does not claim focus without a selected session", async () => {
  const requests = [];
  const document = eventTarget({ hidden: false });
  document.hasFocus = () => true;
  const window = eventTarget({ setInterval() { return 1; } });
  const controller = new NotificationPresenceController(
    document,
    window,
    () => "",
    async (_url, options) => { requests.push(JSON.parse(options.body)); },
    "empty-window"
  );

  controller.start();
  await flush();

  assert.deepEqual(requests, [{ client_id: "empty-window", session: "", focused: false, sequence: 1 }]);
});

test("a voice call keeps its session in use while the page is away", async () => {
  const requests = [];
  const document = eventTarget({ hidden: false });
  document.hasFocus = () => true;
  let heartbeat;
  const window = eventTarget({ setInterval(callback) { heartbeat = callback; return 1; } });
  const controller = new NotificationPresenceController(
    document,
    window,
    () => "/sessions/call.jsonl",
    async (_url, options) => { requests.push(JSON.parse(options.body)); },
    "phone"
  );
  controller.start();
  controller.callChanged(true);

  document.hidden = true;
  document.dispatch("visibilitychange");
  await flush();
  assert.deepEqual(requests.at(-1), { client_id: "phone", session: "/sessions/call.jsonl", focused: true, sequence: 2 });
  heartbeat();
  await flush();
  assert.equal(requests.at(-1).sequence, 3);
  assert.equal(requests.at(-1).focused, true);

  controller.callChanged(false);
  await flush();
  assert.deepEqual(requests.at(-1), { client_id: "phone", session: "", focused: false, sequence: 4 });
});

test("the Hermes panel's page keeps its own identity apart from the page around it", async () => {
  const stored = new Map([["gripi:notification-presence-client", "outer-page"], ["gripi:notification-presence-sequence", "7"]]);
  const sessionStorage = { getItem: (key) => stored.get(key) ?? null, setItem: (key, value) => stored.set(key, value) };
  const requests = [];
  const document = eventTarget({ hidden: false });
  document.hasFocus = () => true;
  const window = eventTarget({ sessionStorage, crypto: { randomUUID: () => "panel-page" }, setInterval() { return 1; } });
  const controller = new NotificationPresenceController(
    document,
    window,
    () => "hermes:conversation",
    async (_url, options) => { requests.push(JSON.parse(options.body)); },
    undefined,
    ":hermes-panel"
  );

  controller.start();
  await flush();

  assert.deepEqual(requests, [{ client_id: "panel-page", session: "hermes:conversation", focused: true, sequence: 1 }]);
  assert.equal(stored.get("gripi:notification-presence-client"), "outer-page");
  assert.equal(stored.get("gripi:notification-presence-sequence"), "7");
});

// A page in a desktop app with PC access tells the gateway that the user is at its computer.
function usedPage({ lookedAt = true } = {}) {
  const used = [];
  const reports = [];
  const document = eventTarget({ hidden: false });
  const window = eventTarget({ setInterval(callback) { window.heartbeat = callback; return 1; } });
  const page = {
    session: "/sessions/one.jsonl",
    lookedAt,
    reports,
    used: () => reports.filter((report) => report.used).map((report) => report.session),
    window,
    document
  };
  page.controller = new NotificationPresenceController(
    document,
    window,
    () => page.session,
    async (_url, options) => { reports.push(JSON.parse(options.body)); },
    "desktop-window",
    "",
    () => page.lookedAt
  );
  page.controller.start();
  page.input = (type, fields = {}) => document.dispatch(type, { type, isTrusted: true, target: new FakeElement(), ...fields });
  return page;
}

test("a page reports at once every click in its session, and typing and scrolling at most every 2 seconds", (t) => {
  t.mock.timers.enable({ apis: ["Date"], now: 0 });
  const page = usedPage();

  page.input("pointerdown");
  page.input("pointerdown", { pointerType: "touch" });
  assert.deepEqual(page.used(), ["/sessions/one.jsonl", "/sessions/one.jsonl"]);
  assert.deepEqual(page.reports.at(-1), { client_id: "desktop-window", session: "/sessions/one.jsonl", focused: true, used: true, sequence: 3 });

  t.mock.timers.tick(2_000);
  page.input("keydown");
  page.input("wheel");
  t.mock.timers.tick(1_999);
  page.input("keydown");
  assert.equal(page.used().length, 3);
  t.mock.timers.tick(1);
  page.input("wheel");
  assert.equal(page.used().length, 4);

  // Another session counts at once.
  page.session = "/sessions/two.jsonl";
  page.input("keydown");
  assert.deepEqual(page.used(), ["/sessions/one.jsonl", "/sessions/one.jsonl", "/sessions/one.jsonl", "/sessions/one.jsonl", "/sessions/two.jsonl"]);
});

test("a page reports no use for Gripi's own events, shortcuts, a held key, focus or heartbeats, nor while nobody looks at it", (t) => {
  t.mock.timers.enable({ apis: ["Date"], now: 0 });
  const page = usedPage();

  page.input("pointerdown", { isTrusted: false });
  page.input("keydown", { repeat: true });
  page.input("keydown", { ctrlKey: true, key: "1" });
  page.input("keydown", { metaKey: true, key: "k" });
  page.window.dispatch("focus");
  page.document.dispatch("visibilitychange");
  page.window.heartbeat();
  page.controller.sessionChanged();
  page.lookedAt = false;
  page.input("wheel");
  page.input("pointerdown");

  assert.deepEqual(page.used(), []);
  assert.ok(page.reports.length >= 4);
});

test("a switch to another session that the user starts reports that session, not the one being left, once also on touch", () => {
  const page = usedPage();
  const sidebar = new FakeElement("nav");
  sidebar.className = "session-sidebar";
  const link = new FakeElement("a");
  sidebar.append(link);
  const relations = new FakeElement("details");
  relations.className = "session-relation-tree";
  const palette = new FakeElement("div");
  palette.setAttribute("data-modal", "command-palette-modal");
  const search = new FakeElement("input");
  palette.append(search);

  page.input("pointerdown", { target: link, pointerType: "touch" });
  page.input("touchstart", { target: link });
  page.input("pointerdown", { target: relations });
  page.input("keydown", { target: search });
  page.session = "/sessions/two.jsonl";
  page.controller.sessionChanged({ used: true });

  assert.deepEqual(page.used(), ["/sessions/two.jsonl"]);
  assert.deepEqual(page.reports.at(-1), { client_id: "desktop-window", session: "/sessions/two.jsonl", focused: true, used: true, sequence: 2 });
});

test("a page tells the service worker which session it shows, as its URL may not say", async () => {
  const serviceWorker = eventTarget({});
  const window = eventTarget({ setInterval() {}, navigator: { serviceWorker } });
  const document = eventTarget({ hidden: false });
  document.hasFocus = () => true;
  const controller = new NotificationPresenceController(document, window, () => "/sessions/one.jsonl", async () => {}, "page");
  controller.start();
  const answers = [];
  const port = { postMessage: (answer) => answers.push(answer) };

  serviceWorker.dispatch("message", { data: { type: "gripi-shown-session" }, ports: [port] });
  serviceWorker.dispatch("message", { data: { type: "other" }, ports: [port] });
  document.hidden = true;
  serviceWorker.dispatch("message", { data: { type: "gripi-shown-session" }, ports: [port] });

  assert.deepEqual(answers, ["/sessions/one.jsonl", ""]);
});

function eventTarget(properties) {
  const listeners = new Map();
  return {
    ...properties,
    addEventListener(name, callback) {
      if (!listeners.has(name)) listeners.set(name, []);
      listeners.get(name).push(callback);
    },
    dispatch(name, event = {}) {
      for (const callback of listeners.get(name) || []) callback(event);
    }
  };
}

async function flush() {
  await new Promise((resolve) => setTimeout(resolve, 0));
}
