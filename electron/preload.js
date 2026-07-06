const { contextBridge, ipcRenderer } = require("electron");

contextBridge.exposeInMainWorld("piGatewayDesktop", {
  activateGateway: (id) => ipcRenderer.invoke("gateway-config:activate", id),
  addGateway: (gateway) => ipcRenderer.invoke("gateway-config:add-gateway", gateway),
  getGatewayConfig: () => ipcRenderer.invoke("gateway-config:get"),
  onAddGatewayRequested: (callback) => {
    ipcRenderer.on("gateway:add-requested", callback);
  },
  onGatewayActivationRequested: (callback) => {
    ipcRenderer.on("gateway:activate-requested", (_event, id) => callback(id));
  },
  onRemoveGatewayRequested: (callback) => {
    ipcRenderer.on("gateway:remove-requested", callback);
  },
  onRenameGatewayRequested: (callback) => {
    ipcRenderer.on("gateway:rename-requested", callback);
  },
  removeGateway: (id) => ipcRenderer.invoke("gateway-config:remove-gateway", id),
  saveGateway: (gateway) => ipcRenderer.invoke("gateway-config:save-gateway", gateway)
});
