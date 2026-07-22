import assert from "node:assert/strict";
import { test } from "node:test";

import { ConversationController } from "../public/assets/conversation_controller.js";
import { CurrentSessionFindController } from "../public/assets/current_session_find_controller.js";
import { SidebarController } from "../public/assets/sidebar_controller.js";
import { deferred } from "./helpers/fake_dom.mjs";

test("sidebar ignores stale refreshes and admits only one pin mutation", async () => {
  const originalFetch = globalThis.fetch;
  const document = { hidden: false, querySelector: () => null, querySelectorAll: () => [], activeElement: null };
  const element = { querySelector: () => null };
  const controller = new SidebarController(document, { location: { href: "https://example.test/", origin: "https://example.test" } }, {}, {}, () => {});
  controller.element = element;
  controller.fragmentUrl = () => "/sidebar";
  controller.controlsActive = () => false;
  controller.modalIsOpen = () => false;
  controller.recentlyInteracted = () => false;
  controller.scheduleRefresh = () => {};
  const replacements = [];
  controller.replace = (html) => replacements.push(html);

  const first = deferred();
  const second = deferred();
  globalThis.fetch = (() => {
    const requests = [first, second];
    return () => requests.shift().promise;
  })();
  try {
    const older = controller.refresh();
    const newer = controller.refresh();
    second.resolve({ ok: true, text: async () => "new sidebar" });
    await newer;
    first.resolve({ ok: true, text: async () => "stale sidebar" });
    await older;
    assert.deepEqual(replacements, ["new sidebar"]);

    let pinFetches = 0;
    const pinResponse = deferred();
    globalThis.fetch = () => { pinFetches += 1; return pinResponse.promise; };
    controller.refresh = async ({ force } = {}) => assert.equal(force, true);
    const classes = new Set();
    const attributes = new Map();
    const button = {
      dataset: { pinned: "false", sessionPath: "/session" },
      disabled: false,
      isConnected: true,
      classList: {
        add: (name) => classes.add(name),
        remove: (name) => classes.delete(name),
        toggle: (name, enabled) => enabled ? classes.add(name) : classes.delete(name),
      },
      setAttribute: (name, value) => attributes.set(name, value),
      removeAttribute: (name) => attributes.delete(name),
    };
    const mutation = controller.togglePin(button);
    const overlapping = await controller.togglePin(button);
    assert.equal(overlapping, null);
    pinResponse.resolve({ ok: true, json: async () => ({ pinned: true }) });
    await mutation;
    assert.equal(pinFetches, 1);
    assert.equal(button.dataset.pinned, "true");
    assert.equal(button.disabled, false);
    assert.equal(classes.has("is-loading"), false);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("history pagination and find callers share in-flight work and use the latest query", async () => {
  const originalFetch = globalThis.fetch;
  const historyResponse = deferred();
  let fetches = 0;
  globalThis.fetch = () => { fetches += 1; return historyResponse.promise; };
  try {
    const conversation = new ConversationController({}, { location: { origin: "https://example.test" } });
    conversation.element = {
      dataset: { olderMessageCursor: "20", hasOlderMessages: "true" },
      querySelector: () => null,
    };
    conversation.currentSessionPath = () => "/session";
    conversation.olderConversationUrl = () => "/older";
    conversation.loadingHistoryStatus = () => {};
    conversation.availableHistoryStatus = () => {};
    conversation.finishHistoryStatus = () => {};
    conversation.failHistoryStatus = () => {};
    conversation.prependOlderHtml = async () => {};

    const one = conversation.loadOlderWindow();
    const two = conversation.loadOlderWindow();
    assert.equal(one, two);
    historyResponse.resolve({ ok: true, json: async () => ({ next_cursor: 10, has_older_messages: true, older_message_count: 10, html: "older" }) });
    assert.deepEqual(await Promise.all([one, two]), ["more", "more"]);
    assert.equal(fetches, 1);

    const complete = deferred();
    let historyLoads = 0;
    conversation.loadOlderHistory = () => { historyLoads += 1; return complete.promise; };
    conversation.bindingEpoch = 4;
    const find = new CurrentSessionFindController({}, conversation);
    find.bar = { hidden: false };
    find.input = { value: "first" };
    find.count = { textContent: "" };
    find.historyStatus = "pending";
    find.bindingEpoch = 2;
    const refreshed = [];
    find.refresh = (options) => refreshed.push({ query: find.input.value, options });
    const firstSearch = find.search({ resetIndex: true });
    find.input.value = "latest";
    const secondSearch = find.search({ resetIndex: false });
    complete.resolve("complete");
    await Promise.all([firstSearch, secondSearch]);
    assert.equal(historyLoads, 1);
    assert.deepEqual(refreshed, [{ query: "latest", options: { resetIndex: true } }]);
  } finally {
    globalThis.fetch = originalFetch;
  }
});
