const { app, BrowserWindow, Menu, Notification, clipboard, ipcMain, session, shell } = require("electron");
const path = require("node:path");
const { pathToFileURL } = require("node:url");
const {
  addGateway,
  readOrCreateGatewayConfig,
  removeGateway,
  saveGateway,
  writeGatewayConfig
} = require("./gateway_config");
const { gatewayUrl } = require("./gateway_url");

const PRELOAD_PATH = path.join(__dirname, "preload.js");
const GATEWAY_PRELOAD_PATH = path.join(__dirname, "gateway_preload.js");
const SHELL_PAGE_PATH = path.join(__dirname, "shell.html");
const WINDOW_ICON_PATH = path.join(__dirname, "assets", "icons", "1024x1024.png");
const popupWindows = new Set();
const gatewayWebContents = new Map();

let config = null;
let mainWindow = null;
let pendingWebviews = [];

function createWindow() {
  config = readOrCreateGatewayConfig(gatewayConfigPath());
  config = applyLaunchUrlOverride(config);

  mainWindow = new BrowserWindow({
    width: 1400,
    height: 900,
    title: process.platform === "darwin" ? "" : "GRIPi",
    icon: WINDOW_ICON_PATH,
    autoHideMenuBar: true,
    webPreferences: {
      contextIsolation: true,
      nodeIntegration: false,
      preload: PRELOAD_PATH,
      sandbox: true,
      webviewTag: true
    }
  });

  if (process.platform === "darwin") {
    mainWindow.on("page-title-updated", (event) => event.preventDefault());
  }

  mainWindow.webContents.setWindowOpenHandler(({ url }) => {
    openExternalUrl(url);
    return { action: "deny" };
  });
  mainWindow.webContents.on("will-attach-webview", (event, webPreferences, params) => {
    const allowedOrigin = safeExternalOrigin(params.src);
    if (!allowedOrigin) {
      event.preventDefault();
      return;
    }

    webPreferences.preload = GATEWAY_PRELOAD_PATH;
    webPreferences.contextIsolation = true;
    webPreferences.nodeIntegration = false;
    webPreferences.sandbox = true;
    pendingWebviews.push({ allowedOrigin, partition: webPreferences.partition });
  });
  mainWindow.webContents.on("did-attach-webview", (_event, guestContents) => {
    const webview = pendingWebviews.shift();
    if (webview) installGatewayNavigationGuard(guestContents, webview.allowedOrigin, webview.partition);
  });

  mainWindow.loadURL(pathToFileURL(SHELL_PAGE_PATH).toString());
}

function applyLaunchUrlOverride(currentConfig) {
  const activeGateway = activeGatewayFrom(currentConfig);
  const effectiveUrl = gatewayUrl(process.env, process.argv, activeGateway.url);
  if (effectiveUrl === activeGateway.url) return currentConfig;

  return saveGateway(currentConfig, { ...activeGateway, url: effectiveUrl });
}

function activeGatewayFrom(currentConfig) {
  return currentConfig.gateways.find((gateway) => gateway.id === currentConfig.activeGatewayId) || currentConfig.gateways[0];
}

function gatewayConfigPath() {
  return path.join(app.getPath("userData"), "config.json");
}

function registerGatewayConfigIpc() {
  ipcMain.handle("gateway-config:get", () => config);

  ipcMain.handle("gateway-notification:show", (event, payload) => {
    return showGatewayNotification(event, payload);
  });

  ipcMain.handle("gateway-clipboard:write", (event, text) => {
    return writeGatewayClipboard(event, text);
  });

  ipcMain.handle("gateway-config:activate", (_event, id) => {
    const gateway = config.gateways.find((existingGateway) => existingGateway.id === id);
    if (!gateway) return config;

    config = writeGatewayConfig(gatewayConfigPath(), { ...config, activeGatewayId: gateway.id });
    return config;
  });

  ipcMain.handle("gateway-config:add-gateway", (_event, gateway) => {
    config = writeGatewayConfig(gatewayConfigPath(), addGateway(config, gateway));
    return config;
  });

  ipcMain.handle("gateway-config:save-gateway", (_event, gateway) => {
    config = writeGatewayConfig(gatewayConfigPath(), saveGateway(config, gateway));
    return config;
  });

  ipcMain.handle("gateway-config:remove-gateway", async (_event, id) => {
    config = writeGatewayConfig(gatewayConfigPath(), removeGateway(config, id));
    await clearGatewaySession(id).catch((error) => {
      console.warn(`Could not clear session data for removed gateway ${id}:`, error);
    });
    return config;
  });
}

async function clearGatewaySession(id) {
  const gatewaySession = session.fromPartition(`persist:gripi-${id}`);
  await gatewaySession.clearStorageData();
  await gatewaySession.clearCache();
}

function installAppMenu() {
  const isMac = process.platform === "darwin";
  const template = [
    ...(isMac ? [{ role: "appMenu" }] : []),
    {
      label: "File",
      submenu: [
        {
          label: "New Session…",
          accelerator: "CmdOrCtrl+N",
          click: () => {
            if (mainWindow) mainWindow.webContents.send("gateway:new-session-requested");
          }
        },
        {
          label: "Add Server…",
          accelerator: "CmdOrCtrl+Shift+N",
          click: () => {
            if (mainWindow) mainWindow.webContents.send("gateway:add-requested");
          }
        },
        {
          label: "Rename Current Server…",
          click: () => {
            if (mainWindow) mainWindow.webContents.send("gateway:rename-requested");
          }
        },
        {
          label: "Remove Current Server…",
          click: () => {
            if (mainWindow) mainWindow.webContents.send("gateway:remove-requested");
          }
        },
        {
          label: "Next Server",
          accelerator: "Ctrl+Tab",
          click: () => {
            if (mainWindow) mainWindow.webContents.send("gateway:activate-next-requested");
          }
        },
        { type: "separator" },
        isMac ? { role: "close" } : { role: "quit" }
      ]
    },
    {
      label: "Edit",
      submenu: [
        { role: "undo" },
        { role: "redo" },
        { type: "separator" },
        {
          label: "Find in Session…",
          accelerator: "CmdOrCtrl+F",
          click: (_menuItem, browserWindow) => {
            routeGatewayShortcut(browserWindow, "gateway:find-in-session-requested", "gripi:current-session-find-requested");
          }
        },
        {
          label: "Find Next",
          accelerator: "CmdOrCtrl+G",
          click: (_menuItem, browserWindow) => {
            routeGatewayShortcut(browserWindow, "gateway:find-in-session-navigation-requested", "gripi:current-session-find-navigation-requested", 1);
          }
        },
        {
          label: "Find Previous",
          accelerator: "CmdOrCtrl+Shift+G",
          click: (_menuItem, browserWindow) => {
            routeGatewayShortcut(browserWindow, "gateway:find-in-session-navigation-requested", "gripi:current-session-find-navigation-requested", -1);
          }
        },
        {
          label: "Search Sessions…",
          accelerator: "CmdOrCtrl+Shift+F",
          click: (_menuItem, browserWindow) => {
            routeSessionSearchShortcut(browserWindow);
          }
        },
        { type: "separator" },
        { role: "cut" },
        { role: "copy" },
        { role: "paste" },
        { role: "pasteAndMatchStyle" },
        { role: "delete" },
        { type: "separator" },
        { role: "selectAll" },
        ...(isMac ? [{ type: "separator" }, { role: "startSpeaking" }, { role: "stopSpeaking" }] : [])
      ]
    },
    { role: "viewMenu" },
    { role: "windowMenu" }
  ];

  Menu.setApplicationMenu(Menu.buildFromTemplate(template));
}

function routeGatewayShortcut(browserWindow, channel, eventName, detail) {
  if (browserWindow && browserWindow !== mainWindow && popupWindows.has(browserWindow)) {
    const script = `window.dispatchEvent(new CustomEvent(${JSON.stringify(eventName)}, { detail: ${JSON.stringify(detail)} }))`;
    browserWindow.webContents.executeJavaScript(script, true).catch(() => {});
    return;
  }

  if (mainWindow) mainWindow.webContents.send(channel, detail);
}

function routeSessionSearchShortcut(browserWindow) {
  if (browserWindow && browserWindow !== mainWindow && popupWindows.has(browserWindow)) {
    const gatewayId = gatewayWebContents.get(browserWindow.webContents.id)?.gatewayId;
    if (!mainWindow) return;

    mainWindow.show();
    mainWindow.focus();
    mainWindow.webContents.send("gateway:search-sessions-requested", gatewayId);
    return;
  }

  if (mainWindow) mainWindow.webContents.send("gateway:search-sessions-requested");
}

function installGatewayNavigationGuard(guestContents, allowedOrigin, partition) {
  gatewayWebContents.set(guestContents.id, { allowedOrigin, gatewayId: gatewayIdFromPartition(partition), partition });
  guestContents.once("destroyed", () => gatewayWebContents.delete(guestContents.id));
  installGatewayPermissionHandlers(partition, allowedOrigin);

  guestContents.on("before-input-event", (event, input) => {
    if (input.type !== "keyDown" || !input.control || input.shift || input.alt || input.meta || input.key !== "Tab") return;

    event.preventDefault();
    if (mainWindow) mainWindow.webContents.send("gateway:activate-next-requested");
  });

  guestContents.setWindowOpenHandler(({ url }) => {
    if (sameOrigin(url, allowedOrigin)) return openSameOriginPopupWindow(url, allowedOrigin, partition);

    openExternalUrl(url);
    return { action: "deny" };
  });

  guestContents.on("will-navigate", (event, url) => {
    if (sameOrigin(url, allowedOrigin)) return;

    event.preventDefault();
    openExternalUrl(url);
  });

  guestContents.on("will-redirect", (event, url) => {
    if (sameOrigin(url, allowedOrigin)) return;

    event.preventDefault();
    openExternalUrl(url);
  });
}

function installGatewayPermissionHandlers(partition, allowedOrigin) {
  const gatewaySession = partition ? session.fromPartition(partition) : session.defaultSession;
  const allowedGatewayPermissions = new Set(["notifications", "clipboard-sanitized-write"]);
  gatewaySession.setPermissionCheckHandler((_webContents, permission, requestingOrigin) => {
    return allowedGatewayPermissions.has(permission) && requestingOrigin === allowedOrigin;
  });
  gatewaySession.setPermissionRequestHandler((_webContents, permission, callback, details) => {
    callback(allowedGatewayPermissions.has(permission) && details.requestingOrigin === allowedOrigin);
  });
}

function writeGatewayClipboard(event, text) {
  const sender = event.sender;
  const gateway = gatewayWebContents.get(sender.id);
  if (!gateway || !sameOrigin(sender.getURL(), gateway.allowedOrigin)) return { ok: false };
  if (!sameOrigin(event.senderFrame?.url, gateway.allowedOrigin)) return { ok: false };
  if (typeof text !== "string") return { ok: false };

  clipboard.writeText(text);
  return { ok: true };
}

function showGatewayNotification(event, payload) {
  const sender = event.sender;
  const gateway = gatewayWebContents.get(sender.id);
  if (!gateway || !sameOrigin(sender.getURL(), gateway.allowedOrigin)) return { ok: false };
  if (!sameOrigin(event.senderFrame?.url, gateway.allowedOrigin)) return { ok: false };
  if (!Notification.isSupported()) return { ok: false };

  const title = stringPayloadValue(payload?.title) || "GRIPi";
  const body = stringPayloadValue(payload?.body) || "Notification from GRIPi.";
  const url = resolveSameOriginUrl(payload?.url || "/", gateway.allowedOrigin);
  const notification = new Notification({ title, body, icon: WINDOW_ICON_PATH });
  notification.on("click", () => {
    if (mainWindow) {
      if (gateway.gatewayId) mainWindow.webContents.send("gateway:activate-requested", gateway.gatewayId);
      mainWindow.show();
      mainWindow.focus();
    }
    if (url && !sender.isDestroyed()) sender.loadURL(url).catch(() => {});
  });
  notification.show();
  return { ok: true };
}

function gatewayIdFromPartition(partition) {
  return partition?.match(/^persist:gripi-(.+)$/)?.[1] || null;
}

function stringPayloadValue(value) {
  if (typeof value !== "string") return null;
  const trimmed = value.trim();
  return trimmed ? trimmed.slice(0, 500) : null;
}

function resolveSameOriginUrl(candidateUrl, allowedOrigin) {
  try {
    const url = new URL(candidateUrl, allowedOrigin);
    if (url.origin !== allowedOrigin) return null;
    return url.toString();
  } catch (_error) {
    return null;
  }
}

function openExternalUrl(url) {
  if (!safeExternalUrl(url)) return;
  shell.openExternal(url);
}

function openSameOriginPopupWindow(url, allowedOrigin, partition) {
  const webPreferences = {
    contextIsolation: true,
    nodeIntegration: false,
    sandbox: true
  };
  if (partition) webPreferences.partition = partition;

  const popupWindow = new BrowserWindow({
    width: 1100,
    height: 800,
    icon: WINDOW_ICON_PATH,
    autoHideMenuBar: true,
    webPreferences
  });

  popupWindows.add(popupWindow);
  popupWindow.on("closed", () => popupWindows.delete(popupWindow));
  installGatewayNavigationGuard(popupWindow.webContents, allowedOrigin, partition);
  popupWindow.loadURL(url);

  return { action: "deny" };
}

function sameOrigin(candidateUrl, allowedOrigin) {
  try {
    return new URL(candidateUrl).origin === allowedOrigin;
  } catch (_error) {
    return false;
  }
}

function safeExternalOrigin(candidateUrl) {
  try {
    const url = new URL(candidateUrl);
    if (!["http:", "https:"].includes(url.protocol)) return null;
    return url.origin;
  } catch (_error) {
    return null;
  }
}

function safeExternalUrl(candidateUrl) {
  return Boolean(safeExternalOrigin(candidateUrl));
}

app.whenReady().then(() => {
  registerGatewayConfigIpc();
  installAppMenu();
  createWindow();
});

app.on("window-all-closed", () => {
  if (process.platform !== "darwin") app.quit();
});

app.on("activate", () => {
  if (BrowserWindow.getAllWindows().length === 0) createWindow();
});
