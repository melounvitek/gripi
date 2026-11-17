import assert from "node:assert/strict";
import { test } from "node:test";

import { BrowserAccessRequestController, WorkspaceAccessRequestController } from "../public/assets/access_request_controllers.js";
import { FakeDocument, FakeElement } from "./helpers/fake_dom.mjs";

for (const [kind, Controller] of [["browser", BrowserAccessRequestController], ["workspace", WorkspaceAccessRequestController]]) {
  test(`the ${kind} access prompt keeps the request it shows until it is answered`, async (t) => {
    t.mock.timers.enable({ apis: ["setTimeout"] });
    const document = new FakeDocument();
    document.body.dataset[`${kind}AccessEnabled`] = "true";
    const [overlay, title, meta, allow] = ["overlay", "title", "meta", "allow"].map((name) => new FakeElement("div", [`[data-${kind}-access-${name}]`]));
    overlay.append(title, meta, allow);
    document.body.append(overlay);
    let pending = [];
    const answered = [];
    t.mock.method(globalThis, "fetch", async (url, options = {}) => {
      if (options.method !== "POST") return { ok: true, json: async () => ({ requests: pending }) };
      const code = options.body.get("code");
      answered.push(`${url} ${code}`);
      pending = pending.filter((request) => request.code !== code);
      return { ok: true, json: async () => ({ ok: true }) };
    });
    const controller = new Controller(document);
    const poll = () => {
      t.mock.timers.tick(3000);
      return controller.pollPromise;
    };

    pending = [{ code: "MINE-0001" }];
    await controller.resume();
    assert.match(title.textContent, /MINE-0001/);

    // A request that was denied asks again, ahead of the one being compared.
    pending = [{ code: "THEM-0001" }, { code: "MINE-0001" }];
    await poll();
    assert.match(title.textContent, /MINE-0001/);

    await document.listeners.get("click")[0]({ target: allow });
    assert.deepEqual(answered, [`/${kind}-access/approve MINE-0001`]);
    assert.match(title.textContent, /THEM-0001/);

    // Answered in another tab.
    pending = [{ code: "NEXT-0001" }];
    await poll();
    assert.match(title.textContent, /NEXT-0001/);
  });
}
