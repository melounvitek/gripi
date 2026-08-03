import assert from "node:assert/strict";
import { test } from "node:test";

import "../public/assets/notification_preview.js";
import "../public/assets/web_push_worker.js";

test("completed-reply pushes are suppressed only for the focused target session", async () => {
  const shown = [];
  const worker = fakeWorker(shown, [
    { focused: true, url: "https://gripi.test/?session=%2Fsessions%2Fcurrent.jsonl" },
    { focused: false, url: "https://gripi.test/?session=%2Fsessions%2Fbackground.jsonl" }
  ]);

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
    badge: "/app-icon.svg",
    data: { url: "/?session=%2Fsessions%2Fbackground.jsonl" }
  }]);
});

test("test pushes remain visible while the notification page is focused", async () => {
  const shown = [];
  const worker = fakeWorker(shown, [{ focused: true, url: "https://gripi.test/notification-test" }]);

  const displayed = await globalThis.gripiDisplayPushNotification({
    type: "gripi-notification-test",
    title: "Gripi test",
    body: "Notifications work.",
    url: "/notification-test"
  }, worker);

  assert.equal(displayed, true);
  assert.equal(shown.length, 1);
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
