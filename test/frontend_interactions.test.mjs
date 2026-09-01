import assert from "node:assert/strict";
import { test } from "node:test";

import { ConversationController } from "../public/assets/conversation_controller.js";
import { renderTextWithLinks } from "../public/assets/dom.js";
import { ProjectSelectController } from "../public/assets/project_select_controller.js";
import { SessionActionsController } from "../public/assets/session_actions_controller.js";
import { SidebarController } from "../public/assets/sidebar_controller.js";
import { TREE_FILTERS, TREE_SUMMARY_CHOICES, TreeSessionController, TreeSessionModel } from "../public/assets/tree_session_controller.js";
import { FakeDocument, FakeElement, FakeEventTarget } from "./helpers/fake_dom.mjs";

test("plain message URLs become safe new-window links without changing surrounding text", () => {
  const document = new FakeDocument();
  const body = new FakeElement("pre");
  const text = "Open https://example.test/tasks?q=1, then http://localhost:8080/a_(b). See “https://example.test/quoted”. <script> javascript:alert(1)";

  renderTextWithLinks(body, text, document);

  const links = body.children.filter((child) => child.tagName === "A");
  assert.deepEqual(links.map((link) => link.textContent), ["https://example.test/tasks?q=1", "http://localhost:8080/a_(b)", "https://example.test/quoted"]);
  assert.deepEqual(links.map((link) => link.getAttribute("href")), ["https://example.test/tasks?q=1", "http://localhost:8080/a_(b)", "https://example.test/quoted"]);
  for (const link of links) {
    assert.equal(link.getAttribute("target"), "_blank");
    assert.equal(link.getAttribute("rel"), "nofollow noreferrer noopener");
  }
  assert.equal(body.children.map((child) => child.textContent).join(""), text);
});

test("background reply notifications preserve literal Markdown punctuation", () => {
  const originalWindow = globalThis.window;
  const window = { location: { href: "https://example.test/?session=current", origin: "https://example.test", search: "?session=current" } };
  const notifications = [];
  const controller = new SidebarController({}, window, {}, {}, (...notification) => notifications.push(notification));
  const link = {
    dataset: {
      assistantResponseCount: "2",
      latestAssistantResponsePreview: "Finished feat/my_branch_name",
      sessionPath: "background",
    },
    querySelector: () => ({ textContent: "Background task" }),
  };
  controller.element = { querySelector: () => null, querySelectorAll: () => [link] };
  globalThis.window = window;

  try {
    controller.notifyBackgroundFinalReplies(new Map([["background", 1]]));
  } finally {
    if (originalWindow === undefined) delete globalThis.window;
    else globalThis.window = originalWindow;
  }

  assert.deepEqual(notifications, [["Background task", "Finished feat/my_branch_name", "/?session=background", "gripi-final-reply:background"]]);
});

test("session actions open from the first button tap and from right click", () => {
  const document = new FakeDocument();
  const window = { innerWidth: 400, innerHeight: 800 };
  const row = new FakeElement("div", [".session-row"]);
  row.dataset.sessionPath = "/sessions/one.jsonl";
  row.dataset.sessionName = "One";
  row.dataset.current = "false";
  row.dataset.busy = "false";
  row.dataset.pinned = "false";
  const toggle = new FakeElement("button", ["[data-session-actions-toggle]"]);
  row.append(toggle);
  const menu = new FakeElement("div", ["[data-session-actions-menu]"]);
  menu.hidden = true;
  const rename = new FakeElement("button");
  const pin = new FakeElement("button", ["[data-session-action-pin]"]);
  const remove = new FakeElement("button", ["[data-session-action-delete]"]);
  menu.append(rename, pin, remove);
  document.body.append(row, menu);
  const controller = new SessionActionsController(document, window, {});
  controller.initialize();

  let prevented = false;
  document.listeners.get("click")[0]({ target: toggle, preventDefault() { prevented = true; }, stopPropagation() {} });
  assert.equal(prevented, true);
  assert.equal(menu.hidden, false);
  assert.equal(controller.target.path, "/sessions/one.jsonl");
  assert.equal(toggle.getAttribute("aria-expanded"), "true");
  assert.equal(pin.textContent, "Pin");
  assert.equal(remove.getAttribute("aria-disabled"), "false");

  document.activeElement = pin;
  document.listeners.get("keydown")[0]({ key: "ArrowDown", target: pin, preventDefault() {} });
  assert.equal(remove.focused, true);

  pin.disabled = true;
  remove.focused = false;
  document.activeElement = rename;
  document.listeners.get("keydown")[0]({ key: "ArrowDown", target: rename, preventDefault() {} });
  assert.equal(remove.focused, true);

  controller.closeMenu();
  prevented = false;
  document.listeners.get("contextmenu")[0]({ target: row, clientX: 24, clientY: 30, preventDefault() { prevented = true; } });
  assert.equal(prevented, true);
  assert.equal(menu.hidden, false);
  assert.equal(menu.style.left, "24px");
  assert.equal(menu.style.top, "30px");

  prevented = false;
  let propagationStopped = false;
  document.listeners.get("keydown")[0]({ key: "Escape", target: menu, preventDefault() { prevented = true; }, stopImmediatePropagation() { propagationStopped = true; } });
  assert.equal(prevented, true);
  assert.equal(propagationStopped, true);
  assert.equal(menu.hidden, true);
  assert.equal(toggle.getAttribute("aria-expanded"), "false");
  assert.equal(toggle.focused, true);
});

test("direct session pin activates on the first click", async () => {
  const document = new FakeDocument();
  const row = new FakeElement("div", [".session-row"]);
  row.dataset.sessionPath = "/sessions/one.jsonl";
  row.dataset.sessionName = "One";
  row.dataset.current = "false";
  row.dataset.busy = "false";
  row.dataset.pinned = "false";
  const pin = new FakeElement("button", ["[data-session-pin-toggle]"]);
  row.append(pin);
  document.body.append(row);
  const controller = new SessionActionsController(document, {}, {});
  const targets = [];
  controller.togglePin = async (target) => targets.push(target);
  controller.initialize();

  let prevented = false;
  let propagationStopped = false;
  document.listeners.get("click")[0]({
    target: pin,
    preventDefault() { prevented = true; },
    stopPropagation() { propagationStopped = true; },
  });
  await Promise.resolve();

  assert.equal(prevented, true);
  assert.equal(propagationStopped, true);
  assert.equal(targets.length, 1);
  assert.equal(targets[0].path, "/sessions/one.jsonl");
  assert.equal(targets[0].pinned, false);
});

test("session actions show pin failures in the menu", async () => {
  const originalFetch = globalThis.fetch;
  const document = new FakeDocument();
  const row = new FakeElement("div", [".session-row"]);
  row.dataset.sessionPath = "/sessions/one.jsonl";
  row.dataset.sessionName = "One";
  row.dataset.current = "false";
  row.dataset.busy = "false";
  row.dataset.pinned = "false";
  const replacementRow = new FakeElement("div", [".session-row"]);
  Object.assign(replacementRow.dataset, row.dataset);
  const toggle = new FakeElement("button", ["[data-session-actions-toggle]"]);
  replacementRow.append(toggle);
  const menu = new FakeElement("div", ["[data-session-actions-menu]"]);
  menu.hidden = true;
  const pin = new FakeElement("button", ["[data-session-action-pin]"]);
  const error = new FakeElement("p", ["[data-session-actions-error]"]);
  error.hidden = true;
  menu.append(pin, error);
  document.body.append(replacementRow, menu);
  const controller = new SessionActionsController(document, { innerWidth: 400, innerHeight: 800 }, {});
  globalThis.fetch = async () => ({ ok: false });

  try {
    await assert.rejects(controller.togglePin(controller.targetFor(row)), /Could not update pinned session/);
    assert.equal(menu.hidden, false);
    assert.equal(controller.target.row, replacementRow);
    assert.equal(error.hidden, false);
    assert.equal(error.textContent, "Could not update pinned session");
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("session actions explain why the current session cannot be deleted", () => {
  const document = new FakeDocument();
  const menu = new FakeElement("div", ["[data-session-actions-menu]"]);
  menu.hidden = true;
  const remove = new FakeElement("button", ["[data-session-action-delete]"]);
  menu.append(remove);
  document.body.append(menu);
  const row = new FakeElement("div", [".session-row"]);
  row.dataset.sessionPath = "/sessions/current.jsonl";
  row.dataset.sessionName = "Current";
  row.dataset.current = "true";
  row.dataset.busy = "false";
  row.dataset.pinned = "false";
  const controller = new SessionActionsController(document, { innerWidth: 800, innerHeight: 600 }, {});

  controller.openMenu(row, { x: 10, y: 10 });

  assert.equal(remove.getAttribute("aria-disabled"), "true");
  assert.equal(remove.title, "Cannot delete the current session");
});

test("project selector opens on the first valid touch without sticky-hover behavior", () => {
  const document = new FakeDocument();
  const window = new FakeEventTarget();
  window.Event = class { constructor(type, options) { this.type = type; this.bubbles = options?.bubbles; } };
  window.matchMedia = () => ({ matches: true });
  window.innerHeight = 800;
  window.innerWidth = 400;

  const wrapper = new FakeElement("div", ["[data-project-select]"]);
  wrapper.setAttribute("data-project-select-plain", "");
  const select = new FakeElement("select");
  select.setAttribute("aria-label", "Filter sessions by project");
  select.options = [nativeOption("/alpha", "alpha"), nativeOption("/beta", "beta")];
  select.selectedIndex = 0;
  Object.defineProperty(select, "selectedOptions", { get() { return [this.options[this.selectedIndex]]; } });
  wrapper.append(select);
  document.body.append(wrapper);

  const controller = new ProjectSelectController(document, window);
  controller.initialize();
  const state = wrapper._projectSelectState;
  const touch = (x, y) => ({ identifier: 1, clientX: x, clientY: y });
  let prevented = false;
  state.trigger.listeners.get("touchstart")[0]({ touches: [touch(10, 10)] });
  state.trigger.listeners.get("touchend")[0]({ changedTouches: [touch(12, 12)], preventDefault() { prevented = true; } });

  assert.equal(prevented, true);
  assert.equal(state.trigger.getAttribute("aria-expanded"), "true");
  assert.equal(state.listbox.hidden, false);

  function nativeOption(value, text) {
    const option = new FakeElement("option");
    option.value = value;
    option.textContent = text;
    return option;
  }
});

test("conversation view toggle button reflects the focused state", () => {
  const conversation = new ConversationController({}, {});
  const toggle = new FakeElement("button");
  conversation.viewToggle = toggle;

  conversation.applyFocusedView();
  assert.equal(toggle.dataset.view, "full");
  assert.equal(toggle.getAttribute("aria-pressed"), "false");
  assert.equal(toggle.getAttribute("aria-label"), "Messages-only transcript view");
  assert.equal(toggle.title, "Show messages only");

  conversation.focusedView = true;
  conversation.applyFocusedView();
  assert.equal(toggle.dataset.view, "conversation");
  assert.equal(toggle.getAttribute("aria-pressed"), "true");
  assert.equal(toggle.getAttribute("aria-label"), "Messages-only transcript view");
  assert.equal(toggle.title, "Show all details");
});

test("tree model covers search, folding, navigation, labels, and exact filters", () => {
  assert.deepEqual(TREE_FILTERS.map(({ value }) => value), ["default", "no-tools", "user-only", "labeled-only", "all"]);
  assert.deepEqual(TREE_SUMMARY_CHOICES.map(({ value }) => value), ["none", "default", "custom"]);
  const model = new TreeSessionModel([
    { entryId: "root", parentId: null, role: "user", text: "Start" },
    { entryId: "left", parentId: "root", role: "assistant", text: "Inspect API", current: true },
    { entryId: "leaf", parentId: "left", role: "user", text: "Ship Linux", label: "checkpoint" },
    { entryId: "right", parentId: "root", role: "assistant", text: "Inspect docs" },
  ]);
  assert.equal(model.move("left"), "left");
  assert.deepEqual(model.visibleEntries().map(({ entryId }) => entryId), ["root", "left", "right"]);
  assert.equal(model.move("left"), "root");
  assert.equal(model.move("left"), "root");
  assert.deepEqual(model.visibleEntries().map(({ entryId }) => entryId), ["root"]);
  assert.equal(model.move("right"), "root");
  model.select("left");
  assert.equal(model.move("right"), "left");
  model.setSearch("linux checkpoint");
  assert.deepEqual(model.visibleEntries().map(({ entryId }) => entryId), ["leaf"]);
  assert.deepEqual(model.visibleStructure().roots.map(({ entryId }) => entryId), ["leaf"]);
});

test("tree controller reports navigation failures, posts successful choices, saves labels, and reloads filters", async () => {
  const originalFetch = globalThis.fetch;
  const document = { addEventListener() {} };
  const navigations = [];
  const controller = new TreeSessionController(document, { location: { origin: "https://example.test" } }, {
    currentSessionPath: () => "/session",
    navigate: async (payload, entry) => navigations.push({ payload, entry: entry.entryId }),
  });
  controller.model = new TreeSessionModel([{ entryId: "entry", parentId: null, role: "user", text: "Start" }]);
  controller.syncSelectionControls = () => {};
  const calls = [];
  try {
    globalThis.fetch = async (url, options) => {
      calls.push({ url, body: Object.fromEntries(options.body.entries()) });
      return { ok: false, json: async () => ({ error: "Navigation failed" }) };
    };
    const errorRegion = { textContent: "", hidden: true };
    await controller.navigateEntry(controller.selectedEntry(), "none", "", { errorRegion });
    assert.deepEqual(errorRegion, { textContent: "Navigation failed", hidden: false });

    globalThis.fetch = async (url, options) => {
      calls.push({ url, body: Object.fromEntries(options.body.entries()) });
      return { ok: true, json: async () => url.endsWith("/label") ? ({ label: "release", labelTimestamp: "now" }) : ({ session: "/branched" }) };
    };
    await controller.navigateEntry(controller.selectedEntry(), "custom", "Summarize decisions");
    assert.deepEqual(navigations, [{ payload: { session: "/branched" }, entry: "entry" }]);
    let reloads = 0;
    controller.load = async () => { reloads += 1; };
    controller.setStatus = () => {};
    await controller.saveLabel("release");
    assert.equal(controller.selectedEntry().label, "release");
    assert.equal(reloads, 1);

    controller.model = new TreeSessionModel([{ entryId: "old" }]);
    controller.load = async () => { reloads += 1; };
    controller.applyFilterChoice();
    await new Promise((resolve) => setTimeout(resolve, 0));
    assert.equal(controller.filterChosen, true);
    assert.equal(controller.model, null);
    assert.equal(reloads, 2);

    assert.deepEqual(calls.map(({ url, body }) => [url, body.entry_id, body.summary_mode || body.label]), [
      ["/sessions/tree", "entry", "none"],
      ["/sessions/tree", "entry", "custom"],
      ["/sessions/tree/label", "entry", "release"],
    ]);
  } finally {
    globalThis.fetch = originalFetch;
  }
});
