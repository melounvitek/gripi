const { app, BrowserWindow, Menu, Notification, ipcMain, session, shell } = require("electron");
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
    title: "Pi Web Gateway",
    autoHideMenuBar: true,
    webPreferences: {
      contextIsolation: true,
      nodeIntegration: false,
      preload: PRELOAD_PATH,
      sandbox: true,
      webviewTag: true
    }
  });

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
  const gatewaySession = session.fromPartition(`persist:pi-gateway-${id}`);
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
    { role: "editMenu" },
    { role: "viewMenu" },
    { role: "windowMenu" }
  ];

  Menu.setApplicationMenu(Menu.buildFromTemplate(template));
}

function installGatewayNavigationGuard(guestContents, allowedOrigin, partition) {
  gatewayWebContents.set(guestContents.id, { allowedOrigin, gatewayId: gatewayIdFromPartition(partition), partition });
  guestContents.once("destroyed", () => gatewayWebContents.delete(guestContents.id));
  installGatewayPermissionHandlers(partition, allowedOrigin);

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
  gatewaySession.setPermissionCheckHandler((_webContents, permission, requestingOrigin) => {
    if (permission !== "notifications") return false;
    return requestingOrigin === allowedOrigin;
  });
  gatewaySession.setPermissionRequestHandler((_webContents, permission, callback, details) => {
    callback(permission === "notifications" && details.requestingOrigin === allowedOrigin);
  });
}

function showGatewayNotification(event, payload) {
  const sender = event.sender;
  const gateway = gatewayWebContents.get(sender.id);
  if (!gateway || !sameOrigin(sender.getURL(), gateway.allowedOrigin)) return { ok: false };
  if (!sameOrigin(event.senderFrame?.url, gateway.allowedOrigin)) return { ok: false };
  if (!Notification.isSupported()) return { ok: false };

  const title = stringPayloadValue(payload?.title) || "Pi Web Gateway";
  const body = stringPayloadValue(payload?.body) || "Notification from Pi Web Gateway.";
  const url = resolveSameOriginUrl(payload?.url || "/", gateway.allowedOrigin);
  const notification = new Notification({ title, body, icon: path.join(__dirname, "assets", "icons", "1024x1024.png") });
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
  return partition?.match(/^persist:pi-gateway-(.+)$/)?.[1] || null;
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
