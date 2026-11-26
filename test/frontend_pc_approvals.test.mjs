import assert from "node:assert/strict";
import { test } from "node:test";

import { PcApprovalsController } from "../public/assets/pc_approvals_controller.js";
import { FakeDocument, FakeElement, FakeEventTarget } from "./helpers/fake_dom.mjs";

const NOW = Date.parse("2026-11-26T12:00:00Z");

function approval(id, secondsLeft = 120, computer = "laptop") {
  return { id, computer, summary: `SSH login to ${id}.example as git`, expires_at: new Date(NOW + secondsLeft * 1000).toISOString() };
}

function reply(status, body = {}) {
  return { ok: status === 200, status, json: async () => body };
}

function fixture(replies = []) {
  const document = new FakeDocument();
  const element = new FakeElement("div");
  element.hidden = true;
  const window = new FakeEventTarget();
  const sent = [];
  window.fetch = async (url, options) => {
    sent.push({ url, body: options.body.toString() });
    const next = replies.shift() || reply(200, { ok: true });
    if (next instanceof Error) throw next;
    return next;
  };
  const controller = new PcApprovalsController(document, window);
  controller.bind(element);
  const text = (node) => node.children?.length ? node.children.map(text).join("") : node.textContent;
  const lines = () => element.children.map((row) => ({
    text: text(row.querySelector(".pc-approval-text")),
    time: row.querySelector(".pc-approval-time").textContent
  }));
  const cancel = async (index) => {
    element.children[index].querySelector("button").listeners.get("click").forEach((listener) => listener({}));
    await new Promise(setImmediate);
  };
  return { controller, element, sent, lines, cancel };
}

test("shows a line for each request that a computer asks to approve, with the time left, until it ends or expires", (t) => {
  t.mock.timers.enable({ apis: ["setInterval", "Date"], now: NOW });
  const { controller, element, lines } = fixture();

  controller.update([approval("one", 107), approval("two", 30, "")]);
  assert.equal(element.hidden, false);
  assert.deepEqual(lines(), [
    { text: "Waiting for you on laptop: SSH login to one.example as git", time: "expires in 1:47" },
    { text: "Waiting for you on your computer: SSH login to two.example as git", time: "expires in 0:30" }
  ]);
  t.mock.timers.tick(1000);
  assert.deepEqual(lines().map((line) => line.time), ["expires in 1:46", "expires in 0:29"]);

  const first = element.children[0];
  controller.update([approval("one", 107)]);
  assert.equal(element.children.length, 1);
  assert.equal(element.children[0], first);

  t.mock.timers.tick(106_000);
  assert.equal(element.hidden, true);
  assert.equal(element.children.length, 0);
});

test("cancel refuses the request, and a poll from before then doesn't bring it back", async (t) => {
  t.mock.timers.enable({ apis: ["setInterval", "Date"], now: NOW });
  const { controller, element, sent, cancel } = fixture();
  controller.update([approval("one"), approval("two")]);

  await cancel(1);
  assert.deepEqual(sent, [{ url: "/pc/cancel", body: "id=two" }]);
  assert.equal(element.children.length, 1);

  controller.update([approval("one"), approval("two")]);
  assert.equal(element.children.length, 1);
});

test("a cancel that comes too late hides the line, and one that fails says so and may be tried again", async (t) => {
  t.mock.timers.enable({ apis: ["setInterval", "Date"], now: NOW });
  const { controller, element, sent, lines, cancel } = fixture([reply(409, { error: "The request no longer waits for approval." }), new Error("offline")]);
  controller.update([approval("one"), approval("two")]);

  await cancel(0);
  assert.equal(element.children.length, 1);
  controller.update([approval("one"), approval("two")]);
  assert.equal(element.children.length, 1);

  await cancel(0);
  assert.equal(lines()[0].time, "could not cancel");
  t.mock.timers.tick(1000);
  assert.equal(lines()[0].time, "could not cancel");
  await cancel(0);
  assert.equal(element.hidden, true);
  assert.deepEqual(sent.map((request) => request.body), ["id=one", "id=two", "id=two"]);
});

test("another session's view starts without the lines of the one before", (t) => {
  t.mock.timers.enable({ apis: ["setInterval", "Date"], now: NOW });
  const { controller, element } = fixture();
  controller.update([approval("one")]);

  controller.reset();
  const next = new FakeElement("div");
  next.hidden = true;
  controller.bind(next);
  controller.update([]);

  assert.equal(next.hidden, true);
  assert.equal(next.children.length, 0);
});
