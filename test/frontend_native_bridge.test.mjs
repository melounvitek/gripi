import assert from "node:assert/strict";
import test from "node:test";

import { nativeBridgeMethod, nativeNotificationsRequirePermission } from "../public/assets/native_bridge.js";

test("native bridge prefers the iPhone implementation capability by capability", async () => {
  const calls = [];
  const windowObject = {
    gripiNative: {
      copyText(text) {
        calls.push(["native", text, this === windowObject.gripiNative]);
        return Promise.resolve({ ok: true });
      }
    },
    gripiElectron: {
      copyText(text) {
        calls.push(["electron", text]);
        return Promise.resolve({ ok: true });
      },
      showNotification(payload) {
        calls.push(["notification", payload]);
        return Promise.resolve({ ok: true });
      }
    }
  };

  await nativeBridgeMethod(windowObject, "copyText")("Pi 🥧");
  await nativeBridgeMethod(windowObject, "showNotification")({ title: "Done" });

  assert.deepEqual(calls, [
    ["native", "Pi 🥧", true],
    ["notification", { title: "Done" }]
  ]);
});

test("native bridge preserves browser fallbacks when no implementation exists", () => {
  assert.equal(nativeBridgeMethod({}, "copyText"), null);
  assert.equal(nativeBridgeMethod({ gripiNative: {} }, "showNotification"), null);
});

test("only the iPhone bridge requires an explicit native notification permission flow", () => {
  assert.equal(nativeNotificationsRequirePermission({ gripiNative: { notificationsRequirePermission: true } }), true);
  assert.equal(nativeNotificationsRequirePermission({ gripiElectron: {} }), false);
});
