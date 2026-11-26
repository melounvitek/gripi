import assert from "node:assert/strict";
import { test } from "node:test";

import "../public/assets/notification_preview.js";
import "../public/assets/web_push_worker.js";

// A page answers the worker's question with the session that it shows, or not at all, as one from before Gripi asked.
function page(shows, focused = true) {
  return {
    focused,
    postMessage(message, [port]) {
      assert.deepEqual(message, { type: "gripi-shown-session" });
      if (shows !== undefined) port.postMessage(shows);
    }
  };
}

test("completed-reply pushes are suppressed only for the focused target session", async () => {
  const shown = [];
  const worker = fakeWorker(shown, [page("/sessions/current.jsonl"), page("/sessions/background.jsonl", false)]);

  const suppressed = await globalThis.gripiDisplayPushNotification({
    type: "gripi-notification",
    title: "Current",
    body: "Done",
    url: "/?session=%2Fsessions%2Fcurrent.jsonl"
  }, worker);
  const displayed = await globalThis.gripiDisplayPushNotification({
    type: "gripi-notification",
    title: "Background",
    body: "**Finished** `work`",
    tag: "reply:background",
    url: "/?session=%2Fsessions%2Fbackground.jsonl"
  }, worker);

  assert.equal(suppressed, false);
  assert.equal(displayed, true);
  assert.equal(shown.length, 1);
  assert.deepEqual(shown[0], ["Background", {
    body: "Finished work",
    tag: "reply:background",
    renotify: false,
    icon: "/app-icon.svg",
    badge: "/assets/notification-badge.png",
    data: { url: "/?session=%2Fsessions%2Fbackground.jsonl" }
  }]);
});

test("a plain push shows its body as it is, such as the command of a sudo request", async () => {
  const shown = [];
  await globalThis.gripiDisplayPushNotification({
    type: "gripi-notification",
    title: "Server setup",
    body: "sudo: find /srv -name \"*secret*\" -exec echo `id` <b> {} +",
    plain: true,
    url: "/?session=%2Fsessions%2Fsetup.jsonl"
  }, fakeWorker(shown, []));

  assert.equal(shown[0][1].body, "sudo: find /srv -name \"*secret*\" -exec echo `id` <b> {} +");
});

test("test pushes remain visible while the notification page is focused", async () => {
  const shown = [];
  const worker = fakeWorker(shown, [page("")]);

  const displayed = await globalThis.gripiDisplayPushNotification({
    type: "gripi-notification-test",
    title: "Gripi test",
    body: "Notifications work.",
    url: "/notification-test"
  }, worker);

  assert.equal(displayed, true);
  assert.equal(shown.length, 1);
});

test("a push that names its session, whose URL doesn't, is suppressed for the page in front, which says what it shows", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const hermes = { type: "gripi-notification", title: "Hermes", body: "Approve on laptop", plain: true, url: "/hermes", session: "hermes:abc" };
  const shown = [];

  assert.equal(await globalThis.gripiDisplayPushNotification(hermes, fakeWorker(shown, [page("hermes:abc")])), false);
  assert.equal(await globalThis.gripiDisplayPushNotification(hermes, fakeWorker(shown, [page("hermes:abc", false), page("/sessions/other.jsonl")])), true);
  const silent = globalThis.gripiDisplayPushNotification(hermes, fakeWorker(shown, [page()]));
  await new Promise(setImmediate);
  t.mock.timers.tick(1000);
  assert.equal(await silent, true);
  assert.equal(shown.length, 2);
});

function fakeWorker(shown, clientList) {
  return {
    location: { origin: "https://gripi.test" },
    clients: {
      async matchAll(options) {
        assert.deepEqual(options, { type: "window", includeUncontrolled: true });
        return clientList;
      }
    },
    registration: {
      async showNotification(...notification) {
        shown.push(notification);
      }
    }
  };
}
