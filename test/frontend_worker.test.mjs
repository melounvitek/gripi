import assert from "node:assert/strict";
import { test } from "node:test";

import { WorkerController } from "../public/assets/worker_controller.js";
import { WorkerPickerController } from "../public/assets/worker_picker_controller.js";
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
  const noticeDetails = part("[data-worker-notice-details]", "pre");
  const dismiss = part("[data-worker-notice-dismiss]", "button");
  notice.append(noticeTitle, noticeText, noticeDetails, dismiss);
  for (const element of [badge, prompt, notice]) element.hidden = true;
  const form = new FakeElement("form");
  const statusBar = new FakeElement("div");
  const window = new FakeEventTarget();
  const sent = [];
  window.fetch = async (url, options) => {
    sent.push({ url, body: Object.fromEntries(options.body) });
    return replies.shift() || reply(200, { ok: true });
  };
  const calls = { refresh: 0, stateChanged: 0, failed: [], confirms: [], confirm: true };
  window.confirm = (message) => {
    calls.confirms.push(message);
    return calls.confirm;
  };
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
  return { controller, badge, badgeName, badgeState, prompt, parts, proceed, wait, notice, noticeTitle, noticeText, noticeDetails, dismiss, form, statusBar, sent, calls, click, key, facts };
}

const flush = () => new Promise((resolve) => setImmediate(resolve));

test("a session on a connected worker shows it quietly in the header and in the editor's footer, like a Pi status", () => {
  const { controller, badge, badgeName, badgeState, prompt, form, statusBar, calls } = fixture();

  controller.update(connected, []);
  assert.equal(badge.hidden, false);
  assert.equal(badge.dataset.workerState, "connected");
  assert.equal(badge.title, "Runs on beelink · connected · on a private copy of storefront");
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

test("copying the session's copy takes the editor's place, with its stage and how long it has taken", (t) => {
  t.mock.timers.enable({ apis: ["setInterval", "Date"], now: NOW });
  const { controller, prompt, parts, form, badgeState, facts } = fixture();

  controller.update({ ...connected, state: "connecting", copying: at(-12), ports: [] }, []);
  assert.equal(prompt.hidden, false);
  assert.equal(prompt.dataset.mode, "copying");
  assert.equal(form.hasAttribute("data-worker"), true);
  assert.equal(badgeState.textContent, "copying");
  assert.equal(parts.title.textContent, "Moving this session to beelink");
  assert.equal(parts.time.textContent, "0:12");
  assert.equal(parts.progress.hidden, false);
  assert.equal(parts.actions.hidden, true);
  assert.deepEqual(facts(), [
    "copy", "a private copy of storefront, for this session only",
    "now", "connecting to beelink, and copying Pi there the first time",
    "on main", "storefront's other sessions keep running"
  ]);
  t.mock.timers.tick(61_000);
  assert.equal(parts.time.textContent, "1:13");
  controller.update({ ...connected, state: "copying", copying: at(-73), ports: [] }, []);
  assert.equal(facts()[3], "copying the session and its copy of storefront there");

  controller.update(connected, []);
  assert.equal(prompt.hidden, true);
  assert.equal(form.hasAttribute("data-worker"), false);
});

test("a move or return that waits for another session names it in the editor's place", (t) => {
  t.mock.timers.enable({ apis: ["setInterval", "Date"], now: NOW });
  const { controller, prompt, parts, facts } = fixture();
  const waiting = "Fix the cart totals\nwhen a coupon applies twice and the currency rounds the other way";

  controller.update({ ...connected, state: "connected", copying: at(-5), waiting, ports: [] }, []);
  assert.equal(prompt.dataset.mode, "waiting");
  assert.equal(parts.title.textContent, "Waiting for “Fix the cart totals when a coupon applies twice…” to finish");
  assert.equal(parts.time.textContent, "0:05");
  assert.equal(parts.progress.hidden, true);
  assert.equal(parts.actions.hidden, true);
  assert.deepEqual(facts(), [
    "then", "this session moves to beelink, on a private copy of storefront",
    "on main", "new prompts to storefront's sessions wait meanwhile"
  ]);

  controller.update({ ...connected, state: "returning", returning: at(-2), waiting: "Coupons" }, []);
  assert.equal(parts.title.textContent, "Waiting for “Coupons” to finish");
  assert.equal(facts()[1], "this session comes back from beelink with its changes");
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
  assert.equal(parts.title.textContent, "Bringing this session back from beelink");
  assert.deepEqual(facts(), [
    "now", "stopping it there, then bringing its changes to storefront on main",
    "clashes", "main keeps what it changed too; beelink's version is kept aside"
  ]);
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
  assert.equal(parts.title.textContent, "Moving this session to beelink");
  assert.equal(facts()[3], "preparing the copy");
  assert.equal(await moved, false);
  assert.deepEqual(sent, [{ url: "/workers/move", body: { session: SESSION, worker: "beelink" } }]);
  assert.equal(prompt.dataset.mode, "failed");
  assert.equal(parts.title.textContent, "Could not move this session");
  assert.deepEqual(facts(), ["error", reason]);
  assert.equal(proceed.hidden, true);
  assert.equal(wait.textContent, "ok");
  assert.equal(wait.focused, true);

  key("Escape");
  assert.equal(prompt.hidden, true);
});

test("a return that stopped before it finished keeps the editor's place until continue on main finishes it", async (t) => {
  t.mock.timers.enable({ apis: ["setInterval", "Date"], now: NOW });
  const { controller, prompt, parts, proceed, wait, sent, click, facts } = fixture();

  // Another page or gripi brings it back.
  controller.update({ ...connected, state: "returning", returning: at(-3), ports: [] }, []);
  assert.equal(prompt.dataset.mode, "returning");
  assert.equal(parts.time.textContent, "0:03");
  assert.equal(parts.actions.hidden, true);

  controller.update({ ...connected, state: "returning", ports: [] }, []);
  assert.equal(prompt.dataset.mode, "unfinished");
  assert.equal(parts.title.textContent, "Bringing this session back from beelink didn't finish");
  assert.deepEqual(facts(), ["now", "it doesn't start until its return finishes", "next", "continue on main finishes it"]);
  assert.equal(parts.actions.hidden, false);
  assert.equal(proceed.hidden, false);
  assert.equal(proceed.focused, true);
  assert.equal(wait.hidden, true);
  assert.equal(parts.separator.hidden, true);
  click(proceed);
  assert.equal(prompt.dataset.mode, "returning");
  await flush();
  assert.deepEqual(sent, [{ url: "/workers/return", body: { session: SESSION } }]);
});

const notice = {
  repository: "/home/me/Work/storefront", worker: "beelink", aside: "/home/me/.pi/gripi/returns/0a1b2c3d/worker",
  paths: ["/home/me/Work/storefront/src/cart/totals.ts", "/home/me/Work/storefront/.env.local", "refs/heads/coupons"],
  items: [
    { path: "/home/me/Work/storefront/src/cart/totals.ts", reason: "changed on both sides" },
    { path: "/home/me/Work/storefront/.env.local", reason: "deleted on main, changed on the worker" },
    { path: "refs/heads/coupons", reason: "changed on both sides", ref: "refs/gripi/returns/0a1b2c3d/refs/heads/coupons" }
  ],
  refs: ["refs/gripi/returns/0a1b2c3d/refs/heads/coupons"],
  text: "Main kept its own version of what follows. The worker's is in /home/me/.pi/gripi/returns/0a1b2c3d/worker or under the ref named, with the base's and main's in /home/me/.pi/gripi/returns/0a1b2c3d/base and /home/me/.pi/gripi/returns/0a1b2c3d/main to compare, until the record is discarded:\n- /home/me/Work/storefront/src/cart/totals.ts: changed on both sides"
};

test("what a return kept aside shows in the sync banner, naming the folder and the refs, until a confirmed dismiss", async () => {
  const { controller, notice: banner, noticeTitle, noticeText, noticeDetails, dismiss, sent, calls, click } = fixture();

  controller.update(null, [notice]);
  assert.equal(banner.hidden, false);
  assert.equal(noticeTitle.textContent, "3 changes from beelink kept aside.");
  assert.equal(noticeText.textContent,
    "Main kept its own src/cart/totals.ts, .env.local and refs/heads/coupons. The worker's versions, if any, are in ~/.pi/gripi/returns/0a1b2c3d/worker " +
    "and refs/gripi/returns/0a1b2c3d/refs/heads/coupons.");
  // Each item, why main kept its own, and where the worker's is.
  assert.equal(noticeDetails.textContent, notice.text);

  calls.confirm = false;
  click(dismiss);
  await flush();
  assert.deepEqual(calls.confirms, ["Dismissing also deletes what was kept aside: the worker's versions in ~/.pi/gripi/returns/0a1b2c3d/worker and its recovery refs. Main's files stay as they are."]);
  assert.deepEqual(sent, []);
  assert.equal(banner.hidden, false);

  calls.confirm = true;
  click(dismiss);
  await flush();
  assert.deepEqual(sent, [{ url: "/workers/dismiss_notices", body: { session: SESSION } }]);
  assert.equal(banner.hidden, true);
});

test("several returns' notices name every worker and folder, and give every return's details", () => {
  const { controller, noticeTitle, noticeText, noticeDetails } = fixture();
  const later = {
    ...notice, worker: "nuc", aside: "/home/me/.pi/gripi/returns/9f8e7d6c/worker", refs: [], text: "Main kept its own version of what follows…",
    items: [{ path: "/home/me/Work/.worktrees/storefront/coupons", reason: "the worker removed this worktree; main keeps it" }, { path: "stash", reason: "the worker dropped \"wip\", which main keeps" }]
  };

  controller.update(connected, [notice, later]);
  assert.equal(noticeTitle.textContent, "5 changes from beelink and nuc kept aside.");
  assert.equal(noticeText.textContent,
    "Main kept its own src/cart/totals.ts, .env.local, refs/heads/coupons and 2 more. The worker's versions, if any, are in ~/.pi/gripi/returns/0a1b2c3d/worker, " +
    "~/.pi/gripi/returns/9f8e7d6c/worker and refs/gripi/returns/0a1b2c3d/refs/heads/coupons.");
  assert.equal(noticeDetails.textContent, `${notice.text}\n\n${later.text}`);
});

function pickerFixture({ replies = [], forwarded = [] } = {}) {
  const document = new FakeDocument();
  const modal = new FakeElement("div", ['[data-modal="worker-modal"]']);
  modal.hidden = true;
  const parts = Object.fromEntries(["worker-modal-title", "worker-picker", "worker-port-field", "worker-port", "worker-facts", "worker-status", "worker-go", "worker-hint"]
    .map((name) => [name, new FakeElement(name === "worker-port" ? "input" : "span", [`[data-${name}]`])]));
  modal.append(...Object.values(parts));
  document.body.append(modal);
  const window = new FakeEventTarget();
  const sent = [];
  window.fetch = async (url) => {
    sent.push(url);
    return replies.shift();
  };
  const calls = { moves: [], forwards: [], refresh: 0 };
  const picker = new WorkerPickerController(document, window, {
    openModal: (element) => { element.hidden = false; },
    closeModal: (element) => { element.hidden = true; },
    sessionPath: () => SESSION,
    homePath: (path) => path.replace(/^\/home\/me/, "~"),
    move: (worker, repository) => calls.moves.push([worker, repository]),
    forward: async (port) => {
      calls.forwards.push(port);
      return forwarded.shift();
    },
    refresh: () => calls.refresh++
  });
  const fire = (element, type, event = {}) => element.listeners.get(type).forEach((listener) => listener({ preventDefault() {}, ...event }));
  const terms = () => parts["worker-facts"].children.map((child) => child.textContent);
  return { picker, modal, sent, calls, fire, terms, list: parts["worker-picker"], port: parts["worker-port"], go: parts["worker-go"], status: parts["worker-status"] };
}

test("Run on worker lists the workers with their state and what moves, and moves only with its own button", async () => {
  const workers = [{ name: "nuc", destination: "gripi@nuc", state: "unreachable" }, { name: "beelink", destination: "gripi@beelink", state: "connected" }];
  const roots = ["/home/me/Work/storefront", "/home/me/Work/.worktrees/storefront/coupons"];
  const { picker, modal, sent, calls, fire, terms, list, go } = pickerFixture({
    replies: [reply(200, { workers, repository: { main: "/home/me/Work/storefront", roots } })]
  });

  await picker.openMove();
  assert.equal(modal.hidden, false);
  assert.deepEqual(sent, [`/workers?session=${encodeURIComponent(SESSION)}`]);
  const rows = list.children;
  assert.deepEqual(rows.map((row) => row.children.map((part) => part.textContent).slice(1)), [["nuc", "gripi@nuc", "unreachable"], ["beelink", "gripi@beelink", "connected"]]);
  assert.deepEqual(terms(), [
    "moves", "this session only, to a private copy of storefront",
    "copy", "storefront and ~/Work/.worktrees/storefront/coupons",
    "ignored", "files up to 10 MB; folders such as node_modules/ come from the worker's earlier copies",
    "on main", "storefront's other sessions keep running",
    "return", "brings its changes back; main keeps what it changed too, and the worker's version is kept aside"
  ]);
  // The connected worker comes first under the cursor.
  assert.equal(rows[1].getAttribute("aria-selected"), "true");
  assert.equal(go.textContent, "move to beelink");

  // A tap points at a worker at once, without hover, but leaves the move to the button.
  fire(list, "click", { target: rows[0].children[1] });
  assert.equal(rows[0].getAttribute("aria-selected"), "true");
  assert.equal(go.textContent, "move to nuc");
  assert.deepEqual(calls.moves, []);
  fire(modal, "keydown", { key: "ArrowDown" });
  assert.equal(go.textContent, "move to beelink");

  fire(modal, "keydown", { key: "Enter", target: rows[1] });
  assert.equal(modal.hidden, true);
  assert.deepEqual(calls.moves, [["beelink", "/home/me/Work/storefront"]]);
});

test("Run on worker names the session's own worktree when it started in one", async () => {
  const worktree = "/home/me/Work/.worktrees/storefront/coupons";
  const { picker, terms } = pickerFixture({
    replies: [reply(200, { workers: [{ name: "beelink", destination: "gripi@beelink", state: "connected" }], repository: { main: "/home/me/Work/storefront", roots: [worktree] } })]
  });
  await picker.openMove();
  assert.equal(terms()[3], "~/Work/.worktrees/storefront/coupons, with storefront's Git folder");
});

test("Run on worker says how to add a worker when there is none, and why the session can't move", async () => {
  const { picker, status, go } = pickerFixture({ replies: [reply(200, { workers: [], repository: { main: "/home/me/Work/storefront", roots: ["/home/me/Work/storefront"] } })] });
  await picker.openMove();
  assert.match(status.textContent, /gripi worker add user@host/);
  assert.equal(go.hidden, true);

  const refused = pickerFixture({ replies: [reply(409, { error: "/home/me/notes is not in a Git repository; only sessions in one can move to a worker" })] });
  await refused.picker.openMove();
  assert.equal(refused.status.textContent, "/home/me/notes is not in a Git repository; only sessions in one can move to a worker");
  assert.equal(refused.status.classes.has("is-error"), true);
  assert.equal(refused.go.hidden, true);
  assert.deepEqual(refused.list.children, []);
});

test("Forward a port checks the port, shows the gateway's refusal, and closes once the port is forwarded", async () => {
  const { picker, modal, calls, fire, terms, port, go, status } = pickerFixture({
    forwarded: [{ error: "worker beelink is not connected; its ports can be forwarded while its sessions run" }, { port: 3000, local: 3001 }]
  });

  picker.openForward("beelink");
  assert.equal(port.focused, true);
  port.value = "3000";
  fire(port, "input");
  assert.deepEqual(terms(), ["from", "beelink's localhost:3000", "to", "main's localhost:3000, or the next free port"]);

  port.value = "70000";
  fire(go, "click");
  assert.equal(status.textContent, "Enter a port from 1 to 65535.");
  assert.deepEqual(calls.forwards, []);

  port.value = "3000";
  await picker.activate();
  assert.match(status.textContent, /not connected/);
  assert.equal(modal.hidden, false);
  fire(modal, "keydown", { key: "Enter", target: port });
  await flush();
  assert.deepEqual(calls.forwards, [3000, 3000]);
  assert.equal(modal.hidden, true);
  assert.equal(calls.refresh, 1);
});
