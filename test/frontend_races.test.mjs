import assert from "node:assert/strict";
import { test } from "node:test";

import { ConversationController } from "../public/assets/conversation_controller.js";
import { CurrentSessionFindController } from "../public/assets/current_session_find_controller.js";
import { SessionActionsController } from "../public/assets/session_actions_controller.js";
import { SidebarController } from "../public/assets/sidebar_controller.js";
import { deferred, FakeDocument, FakeElement } from "./helpers/fake_dom.mjs";

test("conversation find restores long tool output according to its wrapping policy", () => {
  const restore = (wraps) => {
    const tailNode = { cloneNode: () => ({ textContent: "latest output" }) };
    const body = {
      dataset: { rawText: "x".repeat(2500) },
      isConnected: true,
      removeAttribute() {},
      replaceChildren(...nodes) { this.children = nodes; },
    };
    const collapse = { dataset: { expanded: "true", collapsed: "false", toolOutputWraps: String(wraps) }, isConnected: true };
    const control = { hidden: true };
    const button = { setAttribute(name, value) { this[name] = value; } };
    const tailTemplate = {
      content: {
        childNodes: [tailNode],
        cloneNode: () => ({ childNodes: [tailNode.cloneNode(true)] }),
      },
    };
    const find = new CurrentSessionFindController({}, {});
    find.expandedToolOutput = {
      collapse,
      body,
      tailTemplate,
      control,
      button,
      originalExpanded: undefined,
      originalCollapsed: "true",
      originalControlHidden: false,
      originalAriaExpanded: "false",
      originalBodyNodes: [tailNode],
    };

    find.restoreToolOutput();

    return { body, button, collapse, control };
  };

  const wrapping = restore(true);
  assert.equal(wrapping.collapse.dataset.expanded, undefined);
  assert.equal(wrapping.collapse.dataset.collapsed, "true");
  assert.equal(wrapping.control.hidden, false);
  assert.equal(wrapping.button["aria-expanded"], "false");
  assert.equal(wrapping.body.children[0].textContent, "latest output");

  const nonWrapping = restore(false);
  assert.equal(nonWrapping.collapse.dataset.expanded, undefined);
  assert.equal(nonWrapping.collapse.dataset.collapsed, "false");
  assert.equal(nonWrapping.control.hidden, true);
});

test("sidebar ignores stale refreshes", async () => {
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
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("session actions admit only one pin mutation", async () => {
  const originalFetch = globalThis.fetch;
  const pinResponse = deferred();
  let pinFetches = 0;
  let refreshes = 0;
  const document = new FakeDocument();
  const directPin = new FakeElement("button", ["[data-session-pin-toggle]"]);
  const secondDirectPin = new FakeElement("button", ["[data-session-pin-toggle]"]);
  const menuPin = new FakeElement("button", ["[data-session-action-pin]"]);
  document.body.append(directPin, secondDirectPin, menuPin);
  const pinControls = [directPin, secondDirectPin, menuPin];
  globalThis.fetch = () => { pinFetches += 1; return pinResponse.promise; };
  const controller = new SessionActionsController(document, {}, { refresh: async () => { refreshes += 1; } });
  const target = { path: "/session", pinned: false };

  try {
    const mutation = controller.togglePin(target);
    assert.deepEqual(pinControls.map(({ disabled }) => disabled), [true, true, true]);
    assert.equal(document.body.classList.contains("session-pin-operation-active"), true);

    const replacementPin = new FakeElement("button", ["[data-session-pin-toggle]"]);
    const replacementSidebar = new FakeElement("aside", [".session-sidebar"]);
    replacementSidebar.append(replacementPin);
    document.body.append(replacementSidebar);
    const sidebar = new SidebarController(document, {}, { initialize() {} }, { apply() {} }, () => {});
    sidebar.bind(replacementSidebar);
    pinControls.push(replacementPin);
    assert.equal(replacementPin.disabled, true);

    const overlapping = await controller.togglePin({ path: "/other-session", pinned: true });
    assert.equal(overlapping, null);
    pinResponse.resolve({ ok: true, json: async () => ({ pinned: true }) });
    assert.deepEqual(await mutation, { pinned: true });
    assert.deepEqual(pinControls.map(({ disabled }) => disabled), [false, false, false, false]);
    assert.equal(document.body.classList.contains("session-pin-operation-active"), false);
    assert.equal(pinFetches, 1);
    assert.equal(refreshes, 1);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("obsolete sidebar filter failure does not cancel a newer filter", async () => {
  const originalFetch = globalThis.fetch;
  const originalWindow = globalThis.window;
  const sidebar = {
    classList: { toggle() {} },
    querySelector: () => null,
  };
  const document = {
    body: { classList: { contains: () => false } },
    activeElement: null,
    dispatchEvent() {},
    querySelector: () => null,
  };
  const pushed = [];
  const window = {
    location: { href: "https://example.test/?session=one", origin: "https://example.test", search: "?session=one" },
    history: { state: null, pushState(_state, _title, url) { pushed.push(url); } },
    CustomEvent: class {},
  };
  const controller = new SidebarController(document, window, {}, {}, () => {});
  controller.element = sidebar;
  controller.replace = () => {};
  controller.scheduleRefresh = () => {};

  const firstSidebar = deferred();
  const response = (body) => ({ ok: true, text: async () => body });
  let requests = 0;
  globalThis.window = window;
  globalThis.fetch = () => {
    requests += 1;
    if (requests === 1) return firstSidebar.promise;
    return Promise.resolve(response(requests === 3 ? "new sidebar" : "new modal"));
  };

  try {
    const input = { value: "first" };
    let nativeSubmissions = 0;
    const form = { querySelector: () => input, submit() { nativeSubmissions += 1; } };
    const firstOperation = controller.changeSearchFilter(form).catch(() => form.submit());
    const filterEpoch = controller.asyncEpoch;
    controller.requestRefresh();
    assert.equal(controller.asyncEpoch, filterEpoch);

    input.value = "second";
    await controller.changeSearchFilter(form).catch(() => form.submit());
    firstSidebar.reject(new Error("obsolete failure"));
    await firstOperation;

    assert.equal(nativeSubmissions, 0);
    assert.equal(pushed.length, 1);
    assert.match(String(pushed[0]), /session_search=second/);
    assert.equal(controller.filterOperationActive, false);
  } finally {
    globalThis.fetch = originalFetch;
    if (originalWindow === undefined) delete globalThis.window;
    else globalThis.window = originalWindow;
  }
});

test("history reconciliation is included in preserved viewport measurements", () => {
  const fragment = { querySelectorAll: () => [] };
  const document = {
    createElement(tagName) {
      assert.equal(tagName, "template");
      return { content: fragment, innerHTML: "" };
    },
  };
  const conversation = new ConversationController(document, {});
  const sequence = [];
  let scrollHeight = 1000;
  conversation.bindingEpoch = 1;
  conversation.element = {
    scrollTop: 100,
    get scrollHeight() { return scrollHeight; },
    insertBefore(content) {
      assert.equal(content, fragment);
      sequence.push("insert");
      scrollHeight = 1100;
    },
  };
  conversation.historyReconciler = (root) => {
    assert.equal(root, fragment);
    sequence.push("reconcile");
    scrollHeight = 900;
  };
  conversation.refreshFocusedActivity = () => {};
  conversation.updateJumpControls = () => {};

  conversation.insertHistoryHtml("<article>persisted</article>", {}, true);

  assert.deepEqual(sequence, ["reconcile", "insert"]);
  assert.equal(conversation.element.scrollTop, 200);
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
