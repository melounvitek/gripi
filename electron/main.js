const { app, BrowserWindow, ipcMain, shell } = require("electron");
const path = require("node:path");
const { fileURLToPath, pathToFileURL } = require("node:url");
const { readGatewayUrl, writeGatewayUrl } = require("./gateway_config");
const { gatewayUrl } = require("./gateway_url");

const OFFLINE_PAGE_PATH = path.join(__dirname, "offline.html");
const PRELOAD_PATH = path.join(__dirname, "preload.js");

function createWindow() {
  const configPath = gatewayConfigPath();
  let targetUrl = gatewayUrl(process.env, process.argv, readGatewayUrl(configPath));
  const win = new BrowserWindow({
    width: 1400,
    height: 900,
    minWidth: 900,
    minHeight: 600,
    title: "Pi Web Gateway",
    webPreferences: {
      contextIsolation: true,
      nodeIntegration: false,
      preload: PRELOAD_PATH,
      sandbox: true
    }
  });

  win.webContents.setWindowOpenHandler(({ url }) => {
    openExternalUrl(url);
    return { action: "deny" };
  });

  win.webContents.on("will-navigate", (event, url) => {
    if (sameOrigin(url, targetUrl) || isOfflinePage(url)) return;

    if (isOfflinePage(win.webContents.getURL()) && safeExternalUrl(url)) {
      targetUrl = new URL(url).toString();
      return;
    }

    event.preventDefault();
    openExternalUrl(url);
  });

  win.webContents.on("did-fail-load", (_event, errorCode, errorDescription, validatedUrl, isMainFrame) => {
    if (!isMainFrame || errorCode === -3) return;
    if (!sameOrigin(validatedUrl, targetUrl)) return;

    showOfflinePage(win, targetUrl, errorDescription || `Could not load ${validatedUrl}`);
  });

  win.loadURL(targetUrl);
}

function sameOrigin(candidateUrl, targetUrl) {
  try {
    return new URL(candidateUrl).origin === new URL(targetUrl).origin;
  } catch (_error) {
    return false;
  }
}

function isOfflinePage(candidateUrl) {
  try {
    return fileURLToPath(candidateUrl) === OFFLINE_PAGE_PATH;
  } catch (_error) {
    return false;
  }
}

function openExternalUrl(url) {
  if (!safeExternalUrl(url)) return;
  shell.openExternal(url);
}

function safeExternalUrl(candidateUrl) {
  try {
    return ["http:", "https:"].includes(new URL(candidateUrl).protocol);
  } catch (_error) {
    return false;
  }
}

function showOfflinePage(win, targetUrl, reason) {
  const offlineUrl = pathToFileURL(OFFLINE_PAGE_PATH);
  offlineUrl.searchParams.set("target", targetUrl);
  offlineUrl.searchParams.set("reason", reason);
  win.loadURL(offlineUrl.toString());
}

function gatewayConfigPath() {
  return path.join(app.getPath("userData"), "config.json");
}

function registerGatewayUrlIpc() {
  ipcMain.handle("gateway-url:save", (event, url) => {
    if (!isOfflinePage(event.senderFrame.url)) throw new Error("Gateway URL can only be saved from the offline page.");
    return writeGatewayUrl(gatewayConfigPath(), url);
  });
}

app.whenReady().then(() => {
  registerGatewayUrlIpc();
  createWindow();
});

app.on("window-all-closed", () => {
  if (process.platform !== "darwin") app.quit();
});

app.on("activate", () => {
  if (BrowserWindow.getAllWindows().length === 0) createWindow();
});
