import assert from "node:assert/strict";
import { test } from "node:test";

import { SudoPromptController } from "../public/assets/sudo_prompt_controller.js";
import { FakeElement, FakeEventTarget } from "./helpers/fake_dom.mjs";

const NOW = Date.parse("2026-10-28T12:00:00Z");

function request(id, secondsLeft = 120) {
  return { id, session: "/sessions/a.jsonl", display: `apt install ${id}`, cwd: "/srv/app", expires_at: new Date(NOW + secondsLeft * 1000).toISOString() };
}

function reply(status, body = {}) {
  return { ok: status === 200, status, json: async () => body };
}

function promptFixture({ replies = [] } = {}) {
  const form = new FakeElement("form");
  const prompt = new FakeElement("section");
  prompt.hidden = true;
  const parts = Object.fromEntries(["expiry", "command", "cwd", "password", "error", "run", "cancel"].map((name) => [name, new FakeElement("span", [`[data-sudo-${name}]`])]));
  prompt.append(...Object.values(parts));
  const window = new FakeEventTarget();
  const sent = [];
  window.fetch = async (url, options) => {
    sent.push({ url, body: options.body.toString() });
    return replies.shift() || reply(200, { ok: true });
  };
  const controller = new SudoPromptController(window, { canFocus: () => true });
  controller.bind(prompt, form);
  const fire = (element, type, event = {}) => element.listeners.get(type).forEach((listener) => listener(event));
  const key = (name, target = parts.password) => {
    const event = { key: name, target, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; } };
    fire(prompt, "keydown", event);
    return event;
  };
  return { controller, form, prompt, window, sent, fire, key, ...parts };
}

const flush = () => new Promise((resolve) => setImmediate(resolve));

test("the oldest pending request takes the editor's place and keeps what is typed while it stays pending", (t) => {
  t.mock.timers.enable({ apis: ["setInterval", "setTimeout", "Date"], now: NOW });
  const { controller, window, form, prompt, command, cwd, expiry, password, fire } = promptFixture();

  controller.update([request("first"), request("second", 150)]);
  assert.equal(prompt.hidden, false);
  assert.equal(form.hasAttribute("data-sudo"), true);
  assert.equal(command.textContent, "apt install first");
  assert.equal(cwd.textContent, "/srv/app");
  assert.equal(expiry.textContent, "expires in 2:00");
  assert.equal(password.focused, true);
  t.mock.timers.tick(61_000);
  assert.equal(expiry.textContent, "expires in 0:59");

  password.value = "typed";
  controller.update([request("first"), request("second", 150)]);
  assert.equal(password.value, "typed");

  // Answered in another tab.
  controller.update([request("second", 150)]);
  assert.equal(command.textContent, "apt install second");
  assert.equal(password.value, "");

  password.value = "typed";
  controller.update([]);
  assert.equal(prompt.hidden, true);
  assert.equal(form.hasAttribute("data-sudo"), false);
  assert.equal(password.value, "");
  // The rest of the password could follow, which the editor must not take.
  assert.equal(controller.dismissed, true);
  fire(window, "pointerdown");
  assert.equal(controller.dismissed, false);
});

test("Enter sends the password as a form and clears the field at once, and a refused password keeps the request", async (t) => {
  t.mock.timers.enable({ apis: ["setInterval", "Date"], now: NOW });
  const { controller, prompt, error, password, sent, key } = promptFixture({ replies: [reply(400, { error: "Enter the password on one line." })] });
  controller.update([request("first")]);

  assert.equal(key("Enter").defaultPrevented, true);
  await flush();
  assert.deepEqual(sent, [{ url: "/sudo/answer", body: "id=first&password=" }]);
  assert.equal(error.textContent, "Enter the password on one line.");
  assert.equal(error.hidden, false);
  assert.equal(prompt.hidden, false);

  password.value = "s3cret &=";
  key("Enter");
  assert.equal(password.value, "");
  await flush();
  assert.equal(sent[1].body, "id=first&password=s3cret+%26%3D");
  assert.equal(prompt.hidden, true);
  assert.equal(controller.dismissed, false);

  // A poll from before the answer still lists it.
  controller.update([request("first")]);
  assert.equal(prompt.hidden, true);
});

test("Escape cancels, and a request that is gone says so before the next one shows", async (t) => {
  t.mock.timers.enable({ apis: ["setInterval", "setTimeout", "Date"], now: NOW });
  const gone = "The sudo request is no longer waiting for an answer.";
  const { controller, prompt, command, error, password, sent, key, cancel } = promptFixture({ replies: [reply(409, { error: gone })] });
  controller.update([request("first"), request("second", 150)]);

  password.value = "half";
  assert.equal(key("Escape", cancel).defaultPrevented, true);
  assert.equal(password.value, "");
  await flush();
  assert.deepEqual(sent, [{ url: "/sudo/cancel", body: "id=first" }]);
  assert.equal(error.textContent, gone);
  controller.update([request("second", 150)]);
  assert.equal(command.textContent, "apt install first");

  t.mock.timers.tick(3000);
  assert.equal(command.textContent, "apt install second");
  assert.equal(error.hidden, true);
  assert.equal(prompt.hidden, false);
});

test("expiry, leaving the page and switching sessions clear the password", (t) => {
  t.mock.timers.enable({ apis: ["setInterval", "Date"], now: NOW });
  const { controller, window, prompt, password, fire } = promptFixture();
  controller.update([request("first", 5)]);

  password.value = "typed";
  fire(window, "pagehide");
  assert.equal(password.value, "");
  password.value = "typed";
  fire(window, "pageshow");
  assert.equal(password.value, "");

  password.value = "typed";
  t.mock.timers.tick(5000);
  assert.equal(prompt.hidden, true);
  assert.equal(password.value, "");
  controller.update([request("first", 5)]);
  assert.equal(prompt.hidden, true);

  controller.update([request("second")]);
  password.value = "typed";
  controller.reset();
  assert.equal(password.value, "");
  assert.equal(controller.shown, false);
});
