import assert from "node:assert/strict";
import { test } from "node:test";

import { decodeApplicationServerKey, WebPushController } from "../public/assets/web_push.js";

const publicKey = "BAECAwQ";

test("Web Push permission and subscription happen from the first enable action", async () => {
  const sequence = [];
  const fixture = webPushFixture(sequence);
  const controller = new WebPushController(fixture.window, fixture.navigator, fixture.fetch);

  const enabled = await controller.enable();

  assert.equal(enabled, true);
  assert.equal(controller.enabled(), true);
  assert.equal(sequence[0], "request-permission");
  assert.deepEqual(sequence.slice(1), ["register-worker", "fetch-config", "get-subscription", "subscribe", "store-subscription"]);
  assert.deepEqual(fixture.subscriptionOptions, {
    userVisibleOnly: true,
    applicationServerKey: decodeApplicationServerKey(publicKey)
  });
});

test("Web Push reconciliation reuses and resyncs an existing subscription", async () => {
  const sequence = [];
  const fixture = webPushFixture(sequence, { permission: "granted", existing: true });
  const controller = new WebPushController(fixture.window, fixture.navigator, fixture.fetch);

  assert.equal(await controller.reconcile(), true);
  assert.deepEqual(sequence, ["register-worker", "fetch-config", "get-subscription", "store-subscription"]);
});

test("Web Push disable removes the server record and browser subscription", async () => {
  const sequence = [];
  const fixture = webPushFixture(sequence, { permission: "granted", existing: true });
  const controller = new WebPushController(fixture.window, fixture.navigator, fixture.fetch);
  await controller.reconcile();
  sequence.length = 0;

  await controller.disable();

  assert.equal(controller.enabled(), false);
  assert.equal(fixture.storage.get("gripi:notifications-disabled"), "true");
  assert.deepEqual(sequence, ["get-registration", "get-subscription", "remove-subscription", "unsubscribe"]);
});

test("Web Push respects denied, disabled, and unsupported browser states", async () => {
  const denied = webPushFixture([], { permission: "denied" });
  const deniedController = new WebPushController(denied.window, denied.navigator, denied.fetch);
  assert.equal(await deniedController.enable(), false);

  const disabled = webPushFixture([], { permission: "granted" });
  disabled.storage.set("gripi:notifications-disabled", "true");
  const disabledController = new WebPushController(disabled.window, disabled.navigator, disabled.fetch);
  assert.equal(await disabledController.reconcile(), false);

  const unsupported = webPushFixture([]);
  delete unsupported.window.PushManager;
  const unsupportedController = new WebPushController(unsupported.window, unsupported.navigator, unsupported.fetch);
  assert.equal(unsupportedController.available(), false);
  assert.equal(await unsupportedController.enable(), false);
});

function webPushFixture(sequence, { permission = "default", existing = false } = {}) {
  const storage = new Map();
  const key = decodeApplicationServerKey(publicKey);
  let currentSubscription = existing ? subscription(sequence, key) : null;
  const fixture = {
    storage,
    subscriptionOptions: null,
  };
  const pushManager = {
    async getSubscription() {
      sequence.push("get-subscription");
      return currentSubscription;
    },
    async subscribe(options) {
      sequence.push("subscribe");
      fixture.subscriptionOptions = options;
      currentSubscription = subscription(sequence, options.applicationServerKey);
      return currentSubscription;
    }
  };
  const registration = { pushManager };
  const serviceWorker = {
    async register() {
      sequence.push("register-worker");
      return registration;
    },
    ready: Promise.resolve(registration),
    async getRegistration() {
      sequence.push("get-registration");
      return registration;
    }
  };
  const Notification = {
    permission,
    async requestPermission() {
      sequence.push("request-permission");
      this.permission = "granted";
      return "granted";
    }
  };
  fixture.window = {
    Notification,
    PushManager: class {},
    localStorage: {
      getItem(name) { return storage.get(name) || null; },
      setItem(name, value) { storage.set(name, value); },
      removeItem(name) { storage.delete(name); }
    }
  };
  fixture.navigator = { serviceWorker };
  fixture.fetch = async (url, options = {}) => {
    if (url === "/web-push/config") {
      sequence.push("fetch-config");
      return response({ public_key: publicKey });
    }
    if (options.method === "PUT") sequence.push("store-subscription");
    if (options.method === "DELETE") sequence.push("remove-subscription");
    return response(null, 204);
  };
  return fixture;
}

function subscription(sequence, applicationServerKey) {
  return {
    endpoint: "https://push.example/device",
    options: { applicationServerKey },
    toJSON() {
      return { endpoint: this.endpoint, keys: { auth: "auth", p256dh: "p256dh" } };
    },
    async unsubscribe() {
      sequence.push("unsubscribe");
      return true;
    }
  };
}

function response(body, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    async json() { return body; }
  };
}
