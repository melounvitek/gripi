const { app, BrowserWindow, Menu, Notification, clipboard, dialog, ipcMain, session, shell } = require("electron");
const path = require("node:path");
const { pathToFileURL } = require("node:url");
const {
  addGateway,
  allowsPcAccess,
  readOrCreateGatewayConfig,
  removeGateway,
  saveGateway,
  writeGatewayConfig
} = require("./gateway_config");
const { gatewayUrl } = require("./gateway_url");
const { configureSessionExportDownload } = require("./downloads");
const { allowsGatewayNavigation } = require("./navigation");
const { installGatewayPermissionHandlers } = require("./permissions");
const { createPcAccess, linkRequest } = require("./pc_access");

const PRELOAD_PATH = path.join(__dirname, "preload.js");
const GATEWAY_PRELOAD_PATH = path.join(__dirname, "gateway_preload.js");
const SHELL_PAGE_PATH = path.join(__dirname, "shell.html");
const WINDOW_ICON_PATH = path.join(__dirname, "assets", "icons", "1024x1024.png");
const PC_ACCESS_SUPPORTED = process.platform === "linux";
const popupWindows = new Set();
const gatewayWebContents = new Map();
const downloadHandlerSessions = new WeakSet();

let config = null;
let mainWindow = null;
let pendingWebviews = [];
let pcAccessReleased = false;
const pcAccess = createPcAccess({ request: pcLinkRequest, confirm: confirmPcAccess });

function createWindow() {
  config = readOrCreateGatewayConfig(gatewayConfigPath());
  applyConfig(applyLaunchUrlOverride(config));

  mainWindow = new BrowserWindow({
    width: 1400,
    height: 900,
    title: process.platform === "darwin" ? "" : "Gripi",
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

function applyConfig(nextConfig) {
  config = nextConfig;
  if (PC_ACCESS_SUPPORTED) {
    for (const gateway of config.gateways) {
      if (gateway.pcAccessOrigin) pcAccess.start(gateway);
      else pcAccess.stop(gateway.id);
    }
  }
  installAppMenu();
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

    applyConfig(writeGatewayConfig(gatewayConfigPath(), { ...config, activeGatewayId: gateway.id }));
    return config;
  });

  // Only the File menu turns PC access on, after its warning, so these keep what the menu set.
  ipcMain.handle("gateway-config:add-gateway", (_event, gateway) => {
    applyConfig(writeGatewayConfig(gatewayConfigPath(), addGateway(config, { ...gateway, pcAccessOrigin: undefined })));
    return config;
  });

  ipcMain.handle("gateway-config:save-gateway", (_event, gateway) => {
    const pcAccessOrigin = config.gateways.find((existingGateway) => existingGateway.id === gateway?.id)?.pcAccessOrigin;
    applyConfig(writeGatewayConfig(gatewayConfigPath(), saveGateway(config, { ...gateway, pcAccessOrigin })));
    return config;
  });

  ipcMain.handle("gateway-config:remove-gateway", async (_event, id) => {
    applyConfig(writeGatewayConfig(gatewayConfigPath(), removeGateway(config, id)));
    pcAccess.stop(id);
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
  const activeGateway = config ? activeGatewayFrom(config) : null;
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
          label: "Scratch Session",
          accelerator: "CmdOrCtrl+Shift+N",
          click: () => {
            if (mainWindow) mainWindow.webContents.send("gateway:scratch-session-requested");
          }
        },
        {
          label: "Add Server…",
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
        ...(PC_ACCESS_SUPPORTED ? [
          { type: "separator" },
          {
            label: "Allow PC Access for This Server",
            type: "checkbox",
            checked: Boolean(activeGateway?.pcAccessOrigin),
            enabled: Boolean(activeGateway),
            click: (menuItem) => {
              setPcAccess(activeGateway, menuItem.checked).catch((error) => console.warn("Could not change PC access:", error));
            }
          }
        ] : []),
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
        {
          label: "Go to or Run…",
          accelerator: "CmdOrCtrl+K",
          click: (_menuItem, browserWindow) => {
            routeGatewayShortcut(browserWindow, "gateway:command-palette-requested", "gripi:command-palette-requested");
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

async function setPcAccess(gateway, enabled) {
  const pcAccessOrigin = enabled && await confirmPcAccessOptIn(gateway) ? new URL(gateway.url).origin : undefined;
  const gateways = config.gateways.map((existingGateway) => (
    existingGateway.id === gateway.id ? { ...existingGateway, pcAccessOrigin } : existingGateway
  ));
  applyConfig(writeGatewayConfig(gatewayConfigPath(), { ...config, gateways }));
}

async function confirmPcAccessOptIn(gateway) {
  if (!allowsPcAccess(gateway.url)) {
    await dialog.showMessageBox(mainWindow, {
      type: "error",
      message: "PC access needs a secure connection",
      detail: "Gripi allows it only for https:// servers, and for http:// servers on this computer."
    });
    return false;
  }

  const { response } = await dialog.showMessageBox(mainWindow, {
    type: "warning",
    message: `Allow ${gateway.name} to use this computer?`,
    detail: [
      "Agents on this server can then ask to use this computer's SSH keys, and to run commands here as you or as root. You approve each use here, unless you allow a key for 5 minutes, and for commands as root, polkit then checks that it is you, usually with your password. Anyone at this computer while it is unlocked can approve an SSH login or a command as you.",
      "The server sees every public key in your SSH agent, with its comment, and the output of the commands that you run for it.",
      "It works only while Gripi is open."
    ].join("\n\n"),
    buttons: ["Cancel", "Allow"],
    defaultId: 0,
    cancelId: 0,
    noLink: true
  });
  return response === 1;
}

async function pcLinkRequest(gateway, pathname, body, signal) {
  const url = new URL(pathname, gateway.url);
  const cookies = await session.fromPartition(`persist:gripi-${gateway.id}`).cookies.get({ url: url.toString() });
  return linkRequest(url, body, { cookie: cookies.map(({ name, value }) => `${name}=${value}`).join("; "), signal });
}

async function confirmPcAccess({ kind, gateway, sessionName, signal, ...request }) {
  if (!mainWindow || mainWindow.isDestroyed()) return "deny";

  const run = kind === "run";
  const message = run ? `${gateway.name} wants to run a command on this computer` : `${gateway.name} wants to use your SSH key`;
  if (Notification.isSupported()) new Notification({ title: message, body: `Session: ${sessionName}`, icon: WINDOW_ICON_PATH }).show();
  mainWindow.show();
  mainWindow.focus();
  const { key, server, login, command, folder, root } = request;
  const detail = (run ? [
    `Session:  ${sessionName}`,
    `Command:  ${command}`,
    `Folder:   ${folder}`,
    `As:       ${root ? "root" : "you"}`,
    ...(root ? ["", "After Run, polkit checks that it is you, usually with your password, and then runs the command as root, which Gripi can't stop."] : [])
  ] : [
    `Session:  ${sessionName}`,
    `Key:      ${key.type} ${key.fingerprint}${key.comment ? ` (${key.comment})` : ""}`,
    `Server:   ${server.names.join(", ") || "known host"} (in your known_hosts)`,
    `Host key: ${server.hostKeyFingerprint}`,
    `Login:    ${login}`,
    "",
    "Allow for 5 minutes lets this session use this key on this server without asking again."
  ]).join("\n");
  const answers = run ? ["deny", "run"] : ["deny", "once", "grant"];
  const { response } = await dialog.showMessageBox(mainWindow, {
    type: "warning",
    message,
    detail,
    buttons: run ? ["Deny", "Run"] : ["Deny", "Allow once", "Allow for 5 minutes"],
    defaultId: 0,
    cancelId: 0,
    noLink: true,
    signal
  });
  return answers[response];
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
  installGatewayPermissionHandlers(partition ? session.fromPartition(partition) : session.defaultSession, allowedOrigin);
  installGatewayDownloadHandler(partition);

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
    if (allowsGatewayNavigation(url, allowedOrigin)) return;

    event.preventDefault();
    openExternalUrl(url);
  });

  guestContents.on("will-redirect", (event, url) => {
    if (allowsGatewayNavigation(url, allowedOrigin)) return;

    event.preventDefault();
    openExternalUrl(url);
  });
}

function installGatewayDownloadHandler(partition) {
  const gatewaySession = partition ? session.fromPartition(partition) : session.defaultSession;
  if (downloadHandlerSessions.has(gatewaySession)) return;

  downloadHandlerSessions.add(gatewaySession);
  gatewaySession.on("will-download", (_event, item, webContents) => {
    const gateway = gatewayWebContents.get(webContents.id);
    if (!gateway) return;

    configureSessionExportDownload(item, gateway.allowedOrigin, app.getPath("downloads"));
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

  const title = stringPayloadValue(payload?.title) || "Gripi";
  const body = stringPayloadValue(payload?.body) || "Notification from Gripi.";
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

// Releases the links, so the next start needn't wait for them to go stale, without holding up the quit for long.
app.on("before-quit", (event) => {
  if (!PC_ACCESS_SUPPORTED || pcAccessReleased) return;

  event.preventDefault();
  pcAccessReleased = true;
  Promise.race([pcAccess.stopAll(), new Promise((resolve) => setTimeout(resolve, 2000))]).then(() => app.quit());
});

app.on("window-all-closed", () => {
  if (process.platform !== "darwin") app.quit();
});

app.on("activate", () => {
  if (BrowserWindow.getAllWindows().length === 0) createWindow();
});
