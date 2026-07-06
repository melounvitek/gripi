const { contextBridge, ipcRenderer } = require("electron");

contextBridge.exposeInMainWorld("piGatewayElectron", {
  showNotification: (payload) => ipcRenderer.invoke("gateway-notification:show", payload)
});
