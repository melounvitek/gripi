const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

const root = path.join(__dirname, "..");

function read(relativePath) {
  return fs.readFileSync(path.join(root, relativePath), "utf8");
}

test("desktop window does not enforce a larger minimum size than a browser tab", () => {
  const main = read("electron/main.js");

  assert.doesNotMatch(main, /minWidth\s*:/);
  assert.doesNotMatch(main, /minHeight\s*:/);
});

test("desktop window hides the native menu bar by default", () => {
  const main = read("electron/main.js");

  assert.match(main, /autoHideMenuBar:\s*true/);
});

test("desktop window hides only its native title on macOS", () => {
  const main = read("electron/main.js");

  assert.match(main, /title:\s*process\.platform === "darwin" \? "" : "GRIPi"/);
  assert.match(main, /if \(process\.platform === "darwin"\) \{\s*mainWindow\.on\("page-title-updated", \(event\) => event\.preventDefault\(\)\);\s*}/s);
});

test("same-origin gateway popups open in an Electron window", () => {
  const main = read("electron/main.js");

  assert.match(main, /if \(sameOrigin\(url, allowedOrigin\)\) return openSameOriginPopupWindow\(url, allowedOrigin, partition\);/);
  assert.match(main, /function openSameOriginPopupWindow\(url, allowedOrigin, partition\)/);
});

test("desktop keeps popup windows alive until they close", () => {
  const main = read("electron/main.js");

  assert.match(main, /const popupWindows = new Set\(\);/);
  assert.match(main, /popupWindows\.add\(popupWindow\);/);
  assert.match(main, /popupWindow\.on\("closed", \(\) => popupWindows\.delete\(popupWindow\)\);/);
});

test("desktop shell chrome remains usable in narrow or short windows", () => {
  const html = read("electron/shell.html");

  assert.match(html, /#tabs\s*{[^}]*overflow-x:\s*auto;/s);
  assert.match(html, /#tabs\s*{[^}]*flex-shrink:\s*0;/s);
  assert.match(html, /\.tab\s*{[^}]*flex:\s*0 1 220px;/s);
  assert.match(html, /\.panel\s*{[^}]*overflow:\s*auto;/s);
  assert.match(html, /\.card\s*{[^}]*box-sizing:\s*border-box;/s);
  assert.match(html, /@media \(max-height: 520px\)/);
});

test("desktop gateway webviews allow target blank popups", () => {
  const shell = read("electron/shell.js");

  assert.match(shell, /webview\.setAttribute\("allowpopups", ""\);/);
});

test("desktop gateway webviews install the notification bridge", () => {
  const main = read("electron/main.js");
  const preload = read("electron/gateway_preload.js");

  assert.match(main, /const GATEWAY_PRELOAD_PATH = path\.join\(__dirname, "gateway_preload\.js"\);/);
  assert.match(main, /webPreferences\.preload = GATEWAY_PRELOAD_PATH;/);
  assert.match(main, /ipcMain\.handle\("gateway-notification:show"/);
  assert.match(main, /event\.senderFrame\?\.url/);
  assert.match(main, /gatewayIdFromPartition/);
  assert.match(main, /gateway:activate-requested/);
  assert.match(main, /new Notification/);
  assert.match(preload, /gripiElectron/);
  assert.match(preload, /gateway-notification:show/);
});

test("desktop gateway webviews allow same-origin clipboard writes", () => {
  const main = read("electron/main.js");

  assert.match(main, /const allowedGatewayPermissions = new Set\(\["notifications", "clipboard-sanitized-write"\]\);/);
  assert.match(main, /allowedGatewayPermissions\.has\(permission\) && requestingOrigin === allowedOrigin/);
  assert.match(main, /allowedGatewayPermissions\.has\(permission\) && details\.requestingOrigin === allowedOrigin/);
});

test("desktop gateway webviews install an app clipboard bridge", () => {
  const main = read("electron/main.js");
  const preload = read("electron/gateway_preload.js");

  assert.match(main, /clipboard/);
  assert.match(main, /ipcMain\.handle\("gateway-clipboard:write"/);
  assert.match(main, /clipboard\.writeText\(text\)/);
  assert.match(main, /sameOrigin\(event\.senderFrame\?\.url, gateway\.allowedOrigin\)/);
  assert.match(preload, /copyText: \(text\) => ipcRenderer\.invoke\("gateway-clipboard:write", text\)/);
  assert.match(preload, /data-copy-target/);
  assert.match(preload, /stopImmediatePropagation\(\)/);
});

test("desktop shell does not reset an existing gateway webview after in-app navigation", () => {
  const shell = read("electron/shell.js");

  assert.match(shell, /existingWebview\.dataset\.gatewayUrl === gateway\.url/);
  assert.match(shell, /sameUrlOrigin\(existingWebview\.dataset\.gatewayUrl, gateway\.url\)/);
  assert.doesNotMatch(shell, /existingWebview\.src !== gateway\.url/);
});

test("desktop server management uses server wording", () => {
  const main = read("electron/main.js");
  const shell = read("electron/shell.js");

  assert.match(main, /Add Server…/);
  assert.match(main, /Rename Current Server…/);
  assert.match(main, /Remove Current Server…/);
  assert.match(shell, /Add Server/);
  assert.match(shell, /New Server/);
  assert.match(shell, /Rename Server/);
  assert.match(shell, /Server URL/);
  assert.match(shell, /Remove server/);
  assert.match(shell, /setupDraft = null;\n  renameDraft = null;\n  config = await window\.gripiDesktop\.activateGateway\(id\);/);
  assert.match(shell, /renameDraft = null;\n  setupDraft = \{ name: "", url: "http:\/\/localhost:4567\/" \};/);
  assert.match(shell, /const currentGateway = config\.gateways\.find\(\(existingGateway\) => existingGateway\.id === gateway\.id\);/);
  assert.doesNotMatch(shell, /window\.prompt/);
  assert.doesNotMatch(shell, /Add Gateway|New Gateway|Gateway URL|Remove gateway/);
});

test("desktop shell shows unread session counts in server tabs", () => {
  const shell = read("electron/shell.js");

  assert.match(shell, /const unreadSessionCounts = new Map\(\);/);
  assert.match(shell, /button\.textContent = gatewayTabLabel\(gateway\);/);
  assert.match(shell, /function gatewayTabLabel\(gateway\)/);
  assert.match(shell, /\.session-sidebar\[data-unread-session-count\]/);
});

test("desktop shell can activate the gateway that emitted a notification", () => {
  const preload = read("electron/preload.js");
  const shell = read("electron/shell.js");

  assert.match(preload, /onGatewayActivationRequested/);
  assert.match(shell, /onGatewayActivationRequested/);
  assert.match(shell, /activateGateway\(id\)/);
});

test("desktop find shortcuts route through the shell and preserve standard editing actions", () => {
  const main = read("electron/main.js");
  const preload = read("electron/preload.js");
  const shell = read("electron/shell.js");

  assert.match(main, /label: "Find in Session…"/);
  assert.match(main, /accelerator: "CmdOrCtrl\+F"/);
  assert.match(main, /gateway:find-in-session-requested/);
  assert.match(main, /label: "Find Next"/);
  assert.match(main, /accelerator: "CmdOrCtrl\+G"/);
  assert.match(main, /label: "Find Previous"/);
  assert.match(main, /accelerator: "CmdOrCtrl\+Shift\+G"/);
  assert.match(main, /gateway:find-in-session-navigation-requested/);
  assert.match(main, /gripi:current-session-find-navigation-requested/);
  assert.match(main, /label: "Search Sessions…"/);
  assert.match(main, /accelerator: "CmdOrCtrl\+Shift\+F"/);
  assert.match(main, /gateway:search-sessions-requested/);
  assert.match(main, /click: \(_menuItem, browserWindow\) => \{/);
  assert.match(main, /routeGatewayShortcut\(browserWindow, "gateway:find-in-session-requested", "gripi:current-session-find-requested"\)/);
  assert.match(main, /routeSessionSearchShortcut\(browserWindow\)/);
  assert.match(main, /function routeGatewayShortcut\(browserWindow, channel, eventName, detail\)/);
  assert.match(main, /popupWindows\.has\(browserWindow\)/);
  assert.match(main, /browserWindow\.webContents\.executeJavaScript/);
  for (const role of ["undo", "redo", "cut", "copy", "paste", "selectAll"]) {
    assert.match(main, new RegExp(`role: "${role}"`));
  }

  assert.match(preload, /onFindInSessionRequested/);
  assert.match(preload, /ipcRenderer\.on\("gateway:find-in-session-requested", \(_event\) => callback\(\)\)/);
  assert.match(preload, /onFindInSessionNavigationRequested/);
  assert.match(preload, /ipcRenderer\.on\("gateway:find-in-session-navigation-requested", \(_event, direction\) => callback\(direction\)\)/);
  assert.match(preload, /onSearchSessionsRequested/);
  assert.match(preload, /ipcRenderer\.on\("gateway:search-sessions-requested", \(_event, gatewayId\) => callback\(gatewayId\)\)/);

  assert.match(shell, /onFindInSessionRequested/);
  assert.match(shell, /dispatchActiveGatewayEvent\("gripi:current-session-find-requested"\)/);
  assert.match(shell, /onFindInSessionNavigationRequested/);
  assert.match(shell, /dispatchActiveGatewayEvent\("gripi:current-session-find-navigation-requested", direction\)/);
  assert.match(shell, /onSearchSessionsRequested/);
  assert.match(shell, /dispatchActiveGatewayEvent\("gripi:session-search-requested"\)/);
  assert.match(shell, /function dispatchActiveGatewayEvent\(eventName, detail\)/);
  assert.match(shell, /if \(!config \|\| setupDraft \|\| renameDraft\) return;/);
  assert.match(shell, /if \(!webview \|\| webview\.hidden\) return;/);
  assert.match(shell, /window\.dispatchEvent\(new CustomEvent/);
});

test("popup find stays local while popup session search activates the shell gateway before dispatch", async () => {
  const main = read("electron/main.js");
  const preload = read("electron/preload.js");
  const callbacks = {};
  const order = [];
  const popupCalls = [];
  const mainCalls = [];
  const popupWindow = {
    webContents: {
      id: 7,
      executeJavaScript: (script, userGesture) => {
        popupCalls.push([script, userGesture]);
        return Promise.resolve();
      }
    }
  };
  const mainWindow = {
    show: () => mainCalls.push(["show"]),
    focus: () => mainCalls.push(["focus"]),
    webContents: { send: (...args) => mainCalls.push(["send", ...args]) }
  };
  const mainContext = vm.createContext({
    gatewayWebContents: new Map([[popupWindow.webContents.id, { gatewayId: "popup" }]]),
    mainWindow,
    popupWindow,
    popupWindows: new Set([popupWindow])
  });
  const context = vm.createContext({
    console,
    CustomEvent: class CustomEvent {},
    document: { getElementById: () => ({}) },
    order,
    window: {
      addEventListener() {},
      gripiDesktop: {
        activateGateway: async (id) => {
          order.push(`activate:${id}`);
          return { activeGatewayId: id, gateways: [{ id: "first" }, { id }] };
        },
        getGatewayConfig: () => new Promise(() => {}),
        onAddGatewayRequested() {},
        onFindInSessionRequested() {},
        onFindInSessionNavigationRequested(callback) { callbacks.navigation = callback; },
        onGatewayActivationRequested() {},
        onNewSessionRequested() {},
        onNextGatewayRequested() {},
        onRemoveGatewayRequested() {},
        onRenameGatewayRequested() {},
        onSearchSessionsRequested(callback) { callbacks.search = callback; }
      }
    }
  });

  const routeFunctions = ["routeGatewayShortcut", "routeSessionSearchShortcut"].map((name) => {
    return main.match(new RegExp(`^function ${name}\\b.*?^}`, "ms"))?.[0];
  });
  assert.ok(routeFunctions.every(Boolean));
  vm.runInContext(routeFunctions.join("\n"), mainContext);

  vm.runInContext(`
    routeGatewayShortcut(popupWindow, "gateway:find-in-session-requested", "gripi:current-session-find-requested");
    routeGatewayShortcut(popupWindow, "gateway:find-in-session-navigation-requested", "gripi:current-session-find-navigation-requested", -1);
    routeSessionSearchShortcut(popupWindow);
  `, mainContext);
  assert.match(popupCalls[0][0], /gripi:current-session-find-requested/);
  assert.match(popupCalls[1][0], /gripi:current-session-find-navigation-requested/);
  assert.match(popupCalls[1][0], /detail/);
  assert.equal(popupCalls[0][1], true);
  assert.equal(popupCalls[1][1], true);
  assert.deepEqual(mainCalls, [
    ["show"],
    ["focus"],
    ["send", "gateway:search-sessions-requested", "popup"]
  ]);
  vm.runInContext('routeGatewayShortcut(mainWindow, "gateway:find-in-session-navigation-requested", "gripi:current-session-find-navigation-requested", 1);', mainContext);
  assert.deepEqual(mainCalls.at(-1), ["send", "gateway:find-in-session-navigation-requested", 1]);

  mainCalls.length = 0;
  mainContext.gatewayWebContents.clear();
  vm.runInContext("routeSessionSearchShortcut(popupWindow); routeSessionSearchShortcut(mainWindow);", mainContext);
  assert.deepEqual(mainCalls, [
    ["show"],
    ["focus"],
    ["send", "gateway:search-sessions-requested", undefined],
    ["send", "gateway:search-sessions-requested"]
  ]);

  assert.match(preload, /ipcRenderer\.on\("gateway:search-sessions-requested", \(_event, gatewayId\) => callback\(gatewayId\)\)/);
  vm.runInContext(read("electron/shell.js"), context);
  vm.runInContext(`
    config = { activeGatewayId: "first", gateways: [{ id: "first" }, { id: "popup" }] };
    render = () => order.push("render");
    dispatchActiveGatewayEvent = (eventName, detail) => order.push("dispatch:" + eventName + ":" + detail);
  `, context);

  callbacks.navigation(-1);
  assert.deepEqual(order, ["dispatch:gripi:current-session-find-navigation-requested:-1"]);

  order.length = 0;
  await callbacks.search("popup");
  assert.deepEqual(order, ["activate:popup", "render", "dispatch:gripi:session-search-requested:undefined"]);

  order.length = 0;
  await callbacks.search();
  assert.deepEqual(order, ["dispatch:gripi:session-search-requested:undefined"]);
});

test("desktop shortcuts separate new sessions from server management", () => {
  const main = read("electron/main.js");
  const preload = read("electron/preload.js");
  const shell = read("electron/shell.js");

  assert.match(main, /label: "New Session…"/);
  assert.match(main, /accelerator: "CmdOrCtrl\+N"/);
  assert.match(main, /gateway:new-session-requested/);
  assert.match(main, /label: "Add Server…"/);
  assert.match(main, /accelerator: "CmdOrCtrl\+Shift\+N"/);
  assert.match(main, /accelerator: "Ctrl\+Tab"/);
  assert.match(main, /guestContents\.on\("before-input-event", \(event, input\) => \{\n    if \(input\.type !== "keyDown" \|\| !input\.control \|\| input\.shift \|\| input\.alt \|\| input\.meta \|\| input\.key !== "Tab"\) return;\n\n    event\.preventDefault\(\);\n    if \(mainWindow\) mainWindow\.webContents\.send\("gateway:activate-next-requested"\);\n  \}\);/);
  assert.match(main, /gateway:activate-next-requested/);
  assert.match(preload, /onNewSessionRequested/);
  assert.match(preload, /onNextGatewayRequested/);
  assert.match(shell, /onNewSessionRequested/);
  assert.match(shell, /openActiveGatewayNewSessionModal/);
  assert.match(shell, /onNextGatewayRequested/);
  assert.match(shell, /activateNextGateway/);
  assert.match(shell, /focusActiveGatewayPrompt/);
  assert.match(shell, /webview\.focus\(\);/);
  assert.match(shell, /window\.dispatchEvent\(new CustomEvent\("gripi:desktop-server-activated"\)\)/);
});
