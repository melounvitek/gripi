const { contextBridge, ipcRenderer } = require("electron");

contextBridge.exposeInMainWorld("piGatewayDesktop", {
  activateGateway: (id) => ipcRenderer.invoke("gateway-config:activate", id),
  addGateway: (gateway) => ipcRenderer.invoke("gateway-config:add-gateway", gateway),
  getGatewayConfig: () => ipcRenderer.invoke("gateway-config:get"),
  onAddGatewayRequested: (callback) => {
    ipcRenderer.on("gateway:add-requested", callback);
  },
  onFindInSessionRequested: (callback) => {
    ipcRenderer.on("gateway:find-in-session-requested", (_event) => callback());
  },
  onFindInSessionNavigationRequested: (callback) => {
    ipcRenderer.on("gateway:find-in-session-navigation-requested", (_event, direction) => callback(direction));
  },
  onGatewayActivationRequested: (callback) => {
    ipcRenderer.on("gateway:activate-requested", (_event, id) => callback(id));
  },
  onNewSessionRequested: (callback) => {
    ipcRenderer.on("gateway:new-session-requested", callback);
  },
  onNextGatewayRequested: (callback) => {
    ipcRenderer.on("gateway:activate-next-requested", callback);
  },
  onRemoveGatewayRequested: (callback) => {
    ipcRenderer.on("gateway:remove-requested", callback);
  },
  onRenameGatewayRequested: (callback) => {
    ipcRenderer.on("gateway:rename-requested", callback);
  },
  onSearchSessionsRequested: (callback) => {
    ipcRenderer.on("gateway:search-sessions-requested", (_event, gatewayId) => callback(gatewayId));
  },
  removeGateway: (id) => ipcRenderer.invoke("gateway-config:remove-gateway", id),
  saveGateway: (gateway) => ipcRenderer.invoke("gateway-config:save-gateway", gateway)
});
