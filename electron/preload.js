const { contextBridge, ipcRenderer } = require("electron");

contextBridge.exposeInMainWorld("piGatewayDesktop", {
  saveGatewayUrl: (url) => ipcRenderer.invoke("gateway-url:save", url)
});
