import assert from "node:assert/strict";
import { test } from "node:test";

import { NotificationPresenceController } from "../public/assets/notification_presence.js";

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
    client_id: "desktop-window", session: "/sessions/one.jsonl", focused: true
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
    client_id: "desktop-window", session: "", focused: false
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

  assert.deepEqual(requests, [{ client_id: "empty-window", session: "", focused: false }]);
});

function eventTarget(properties) {
  const listeners = new Map();
  return {
    ...properties,
    addEventListener(name, callback) {
      if (!listeners.has(name)) listeners.set(name, []);
      listeners.get(name).push(callback);
    },
    dispatch(name) {
      for (const callback of listeners.get(name) || []) callback();
    }
  };
}

async function flush() {
  await new Promise((resolve) => setTimeout(resolve, 0));
}
