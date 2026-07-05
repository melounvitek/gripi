const tabs = document.getElementById("tabs");
const content = document.getElementById("content");

let config = null;
let setupDraft = null;
const webviews = new Map();
const offlineGateways = new Map();
const loadingGateways = new Map();
let isRemovingGateway = false;

window.addEventListener("error", (event) => {
  showFatalError(event.error?.message || event.message || "Unexpected desktop shell error.");
});

window.addEventListener("unhandledrejection", (event) => {
  showFatalError(event.reason?.message || "Unexpected desktop shell error.");
});

window.piGatewayDesktop.onAddGatewayRequested(() => {
  setupDraft = { name: "", url: "http://localhost:4567/" };
  render();
});

window.piGatewayDesktop.onRemoveGatewayRequested(async () => {
  await removeActiveGateway();
});

loadConfig();

async function loadConfig() {
  config = await window.piGatewayDesktop.getGatewayConfig();
  render();
}

function render() {
  if (!config) return;

  renderTabs();
  renderContent();
}

function renderTabs() {
  const showTabs = config.gateways.length > 1 || setupDraft;
  document.body.classList.toggle("has-tabs", Boolean(showTabs));
  tabs.hidden = !showTabs;
  tabs.replaceChildren();

  if (!showTabs) return;

  for (const gateway of config.gateways) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = `tab${gateway.id === config.activeGatewayId && !setupDraft ? " active" : ""}`;
    button.textContent = gateway.name;
    button.addEventListener("click", async () => {
      setupDraft = null;
      config = await window.piGatewayDesktop.activateGateway(gateway.id);
      render();
    });
    tabs.append(button);
  }

  if (setupDraft) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "tab active";
    button.textContent = "New Server";
    tabs.append(button);
  }
}

function renderContent() {
  ensureWebviews();
  hideAllWebviews();
  removePanels();

  if (setupDraft) {
    content.append(setupPanel(setupDraft));
    return;
  }

  const gateway = activeGateway();
  const offlineReason = offlineGateways.get(gateway.id);
  if (offlineReason) {
    content.append(offlinePanel(gateway, offlineReason));
    return;
  }

  webviews.get(gateway.id).hidden = false;

  if (loadingGateways.has(gateway.id)) {
    content.append(messagePanel("Opening server", `Loading ${gateway.url}…`));
  }
}

function ensureWebviews() {
  const gatewayIds = new Set(config.gateways.map((gateway) => gateway.id));

  for (const [gatewayId, webview] of webviews) {
    if (!gatewayIds.has(gatewayId)) {
      webview.remove();
      webviews.delete(gatewayId);
    }
  }

  for (const gateway of config.gateways) {
    const existingWebview = webviews.get(gateway.id);
    if (existingWebview) {
      if (existingWebview.dataset.gatewayUrl === gateway.url) continue;
      if (sameUrlOrigin(existingWebview.dataset.gatewayUrl, gateway.url)) {
        setGatewayUrl(existingWebview, gateway.url);
        continue;
      }

      existingWebview.remove();
      webviews.delete(gateway.id);
    }

    const webview = document.createElement("webview");
    loadingGateways.set(gateway.id, true);
    webview.setAttribute("allowpopups", "");
    webview.partition = `persist:pi-gateway-${gateway.id}`;
    setGatewayUrl(webview, gateway.url);
    webview.addEventListener("did-attach", () => {
      loadingGateways.delete(gateway.id);
      render();
    });
    webview.addEventListener("did-finish-load", () => {
      loadingGateways.delete(gateway.id);
      render();
    });
    webview.addEventListener("did-fail-load", (event) => {
      if (event.isMainFrame === false || event.errorCode === -3) return;
      loadingGateways.delete(gateway.id);
      offlineGateways.set(gateway.id, event.errorDescription || `Could not load ${gateway.url}`);
      render();
    });
    webviews.set(gateway.id, webview);
    content.append(webview);

    window.setTimeout(() => {
      if (!loadingGateways.has(gateway.id)) return;
      loadingGateways.delete(gateway.id);
      offlineGateways.set(gateway.id, "The embedded server view did not attach. Try rebuilding the app or opening the server in a browser.");
      render();
    }, 5000);
  }
}

function hideAllWebviews() {
  for (const webview of webviews.values()) webview.hidden = true;
}

function removePanels() {
  for (const panel of content.querySelectorAll(".panel")) panel.remove();
}

async function removeActiveGateway() {
  if (!config || setupDraft || isRemovingGateway) return;

  const gateway = activeGateway();
  if (config.gateways.length <= 1) {
    window.alert("Cannot remove the only server.");
    return;
  }

  if (!window.confirm(`Remove server “${gateway.name}”?`)) return;

  isRemovingGateway = true;
  try {
    config = await window.piGatewayDesktop.removeGateway(gateway.id);
    offlineGateways.delete(gateway.id);
    loadingGateways.delete(gateway.id);
    render();
  } catch (error) {
    window.alert(error.message || "Could not remove this server.");
  } finally {
    isRemovingGateway = false;
  }
}

function activeGateway() {
  return config.gateways.find((gateway) => gateway.id === config.activeGatewayId) || config.gateways[0];
}

function messagePanel(title, message) {
  const panel = document.createElement("section");
  panel.className = "panel";
  const card = document.createElement("div");
  card.className = "card";
  const heading = document.createElement("h1");
  heading.textContent = title;
  const body = document.createElement("p");
  body.textContent = message;
  card.append(heading, body);
  panel.append(card);
  return panel;
}

function showFatalError(message) {
  content.replaceChildren(messagePanel("Desktop shell error", message));
}

function setupPanel(draft) {
  return gatewayFormPanel({
    title: "Add Server",
    description: "Add a trusted Pi server URL to open it in its own isolated tab.",
    gateway: draft,
    saveLabel: "Add and Open",
    cancelLabel: config.gateways.length > 0 ? "Cancel" : null,
    onSave: async ({ name, url }) => {
      config = await window.piGatewayDesktop.addGateway({ name, url });
      setupDraft = null;
      render();
    },
    onCancel: () => {
      setupDraft = null;
      render();
    }
  });
}

function offlinePanel(gateway, reason) {
  return gatewayFormPanel({
    title: `${gateway.name} is not reachable`,
    description: "Start the server in a terminal, retry the saved URL, or update this tab to another trusted server URL.",
    gateway,
    saveLabel: "Save and Retry",
    cancelLabel: "Retry",
    details: `${gateway.url} — ${reason}`,
    onSave: async ({ name, url }) => {
      const previousUrl = gateway.url;
      config = await window.piGatewayDesktop.saveGateway({ id: gateway.id, name, url });
      const savedGateway = config.gateways.find((existingGateway) => existingGateway.id === gateway.id) || gateway;
      offlineGateways.delete(gateway.id);
      render();
      if (sameUrlOrigin(previousUrl, savedGateway.url)) retryGatewayUrl(gateway.id, savedGateway.url);
    },
    onCancel: () => {
      offlineGateways.delete(gateway.id);
      reloadGateway(gateway.id);
      render();
    }
  });
}

function gatewayFormPanel({ title, description, gateway, saveLabel, cancelLabel, details, onSave, onCancel }) {
  const panel = document.createElement("section");
  panel.className = "panel";

  const card = document.createElement("form");
  card.className = "card";
  card.innerHTML = `
    <h1></h1>
    <p class="description"></p>
    <label>
      Name
      <input name="name" type="text" autocomplete="off" required>
    </label>
    <label>
      Server URL
      <input name="url" type="url" spellcheck="false" autocomplete="url" required>
    </label>
    <div class="actions"></div>
    <p class="muted"></p>
    <p class="error" role="alert"></p>
  `;

  card.querySelector("h1").textContent = title;
  card.querySelector(".description").textContent = description;
  card.elements.name.value = gateway.name || "";
  card.elements.url.value = gateway.url || "http://localhost:4567/";
  card.querySelector(".muted").textContent = details || "";

  const actions = card.querySelector(".actions");
  const saveButton = document.createElement("button");
  saveButton.type = "submit";
  saveButton.className = "primary";
  saveButton.textContent = saveLabel;
  actions.append(saveButton);

  if (cancelLabel) {
    const cancelButton = document.createElement("button");
    cancelButton.type = "button";
    cancelButton.className = "secondary";
    cancelButton.textContent = cancelLabel;
    cancelButton.addEventListener("click", onCancel);
    actions.append(cancelButton);
  }

  card.addEventListener("submit", async (event) => {
    event.preventDefault();
    card.querySelector(".error").textContent = "";

    try {
      await onSave({
        name: card.elements.name.value,
        url: card.elements.url.value
      });
    } catch (error) {
      card.querySelector(".error").textContent = error.message || "Could not save this server.";
    }
  });

  panel.append(card);
  return panel;
}

function setGatewayUrl(webview, url) {
  webview.dataset.gatewayUrl = url;
  webview.src = url;
}

function sameUrlOrigin(firstUrl, secondUrl) {
  try {
    return new URL(firstUrl).origin === new URL(secondUrl).origin;
  } catch (_error) {
    return false;
  }
}

function retryGatewayUrl(gatewayId, url) {
  const webview = webviews.get(gatewayId);
  if (!webview) return;

  webview.dataset.gatewayUrl = url;
  if (webview.src === url) {
    webview.reload();
  } else {
    webview.src = url;
  }
}

function reloadGateway(gatewayId) {
  const webview = webviews.get(gatewayId);
  if (webview) webview.reload();
}
