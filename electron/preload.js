const { contextBridge, ipcRenderer } = require("electron");

contextBridge.exposeInMainWorld("piGatewayDesktop", {
  activateGateway: (id) => ipcRenderer.invoke("gateway-config:activate", id),
  addGateway: (gateway) => ipcRenderer.invoke("gateway-config:add-gateway", gateway),
  getGatewayConfig: () => ipcRenderer.invoke("gateway-config:get"),
  onAddGatewayRequested: (callback) => {
    ipcRenderer.on("gateway:add-requested", callback);
  },
  onRemoveGatewayRequested: (callback) => {
    ipcRenderer.on("gateway:remove-requested", callback);
  },
  removeGateway: (id) => ipcRenderer.invoke("gateway-config:remove-gateway", id),
  saveGateway: (gateway) => ipcRenderer.invoke("gateway-config:save-gateway", gateway)
});
