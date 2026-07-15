import { ESCAPE_STOP_CONFIRMATION_WINDOW_MS, STALE_SESSION_REFRESH_AFTER_MS } from "./constants.js";
import {
  compactNumber,
  eventErrorText,
  eventStatusText,
  eventTimestamp,
  formatWaitDuration,
  imageAttachmentLabel,
  notificationReplyPreview,
  sessionCloneSlashCommand,
  sessionCompactSlashCommand,
  sessionForkSlashCommand,
  sessionModelSlashCommand,
  sessionNameFromEvent,
  sessionNameSlashCommand,
  sessionNewSlashCommand,
  sessionTreeSlashCommand,
  stableTextHash
} from "./formatting.js";
import { modelSettingsKey, selectedThinkingLevel, supportedThinkingLevels } from "./model.js";
import {
  currentSessionFindNavigationShortcut,
  isCtrlOrMetaShortcut,
  keyboardScrollKey,
  recentSessionShortcutFromEvent,
  sessionSearchShortcut
} from "./shortcuts.js";
import { sessionFragmentUrl } from "./urls.js";
import { GatewayUpdateController } from "./gateway_update_controller.js";
import { BrowserAccessRequestController, WorkspaceAccessRequestController } from "./access_request_controllers.js";
import { ProjectSelectController } from "./project_select_controller.js";
import { NewSessionFormController } from "./new_session_form_controller.js";
import { SidebarController } from "./sidebar_controller.js";
import { ConversationController } from "./conversation_controller.js";
import { CurrentSessionFindController } from "./current_session_find_controller.js";
import { LiveMessageParser } from "./live_message_parser.js";
import { LiveMessageRenderer } from "./live_message_renderer.js";
import { ServerMarkdownRenderer } from "./server_markdown_renderer.js";
import { activateToolOutputRegion, enhanceMarkdownCodeBlocks } from "./dom.js";
import { eventPollingDelay } from "./polling.js";

const gatewayUpdateController = new GatewayUpdateController(document, window);
const browserAccessController = new BrowserAccessRequestController(document);
const workspaceAccessController = new WorkspaceAccessRequestController(document);
const projectSelectController = new ProjectSelectController(document, window);
const newSessionFormController = new NewSessionFormController(document, window, projectSelectController);
const sidebarController = new SidebarController(
  document,
  window,
  projectSelectController,
  gatewayUpdateController,
  (name, body, url, tag) => showGripiNotification(name, body, url, tag).catch(() => {})
);

let conversationPanel = null;
let liveOutput = null;
let promptForm = null;
let abortForm = null;
let promptTextarea = null;
let promptSessionInput = null;
let sendButton = null;
let composerStopButton = null;
let attachButton = null;
let attachmentTray = null;
let imageInput = null;
let composerState = null;
let abortButton = null;
let commandList = null;
let highlightedCommandIndex = 0;
let conversationScroll = null;
let sessionStatusBar = null;
let reconnectBanner = null;
let reconnectButton = null;
let liveAgentRunning = false;
let liveBusySince = null;
let liveErrorSeen = false;
let liveStatusModel = null;
let liveStatusThinking = null;
let modelSettingsModels = [];
let modelSettingsCurrentModel = null;
let modelSettingsCurrentThinking = "off";
let modelSettingsSelectedKey = null;
let modelSettingsOperationGeneration = 0;
let thinkingCyclePending = false;
let pendingImages = [];
let escapeStopConfirmationExpiresAt = 0;
let eventPollTimer = null;
let eventPollInFlight = false;
let eventPollAbortController = null;
let eventPollResumeTimer = null;
let staleSessionRefreshInFlight = false;
let markReadInFlight = false;
let markReadQueued = false;
let markReadAfterVisible = false;
let hiddenAt = null;
let lastEventPollSuccessAt = Date.now();
let lastEventSeq = 0;
let waitingForOutputSince = null;
let waitingForOutputTimer = null;
let waitingForOutputLabel = "Pi is running…";
let emptyEventPollCount = 0;
let sessionViewGeneration = 0;
let sessionSwitchGeneration = 0;
let notificationRegistration = null;
const notifiedFinalReplyKeys = new Set();
const conversationController = new ConversationController(document, window);
const currentSessionFindController = new CurrentSessionFindController(document, conversationController);
const liveMessageParser = new LiveMessageParser(document.body.dataset.homeDir || "");
const serverMarkdownRenderer = new ServerMarkdownRenderer(document, conversationController);
const liveMessageRenderer = new LiveMessageRenderer(document, conversationController, liveMessageParser, serverMarkdownRenderer);

function bindSessionDom() {
  conversationPanel = document.querySelector(".conversation-panel");
  liveOutput = document.getElementById("live-output");
  promptForm = document.querySelector(".prompt-form");
  abortForm = document.getElementById("abort-form");
  promptTextarea = promptForm?.querySelector("textarea") || null;
  promptSessionInput = promptForm?.querySelector('input[name="session"]') || null;
  sendButton = promptForm?.querySelector(".send-button") || null;
  composerStopButton = document.querySelector(".session-header .composer-stop-button") || null;
  attachButton = promptForm?.querySelector(".attach-button") || null;
  attachmentTray = promptForm?.querySelector(".attachment-tray") || null;
  imageInput = promptForm?.querySelector(".image-input") || null;
  composerState = document.querySelector(".composer-state");
  abortButton = document.querySelector(".abort-button");
  commandList = document.getElementById("command-list");
  highlightedCommandIndex = 0;
  conversationController.bind(promptTextarea);
  conversationScroll = conversationController.element;
  liveMessageRenderer.bind();
  currentSessionFindController.bind();
  sessionStatusBar = document.getElementById("session-status-bar");
  const existingModelStatus = sessionStatusBar?.querySelector('[data-status-key="model"] .session-status-value')?.textContent || "";
  const existingModelMatch = existingModelStatus.match(/^(.*?)(?:\s+\(([^)]*)\))?$/);
  liveStatusModel = existingModelMatch?.[1] || null;
  liveStatusThinking = existingModelMatch?.[2] || null;
  reconnectBanner = document.querySelector(".session-reconnect");
  reconnectButton = document.querySelector(".reconnect-button");
  updateNotificationToggle();
  gatewayUpdateController.apply();
}

function editableElement(element) {
  return element?.closest?.("input, textarea, select, [contenteditable]");
}

function currentSessionFindShortcut(event) {
  if (!currentSessionFindController.available || String(event.key || "").toLowerCase() !== "f") return false;
  if (event.altKey || event.shiftKey) return false;
  return !!(event.ctrlKey || event.metaKey);
}

function requestSessionSearch() {
  if (modalIsOpen()) return false;
  return sidebarController.openSearch();
}

function handleSessionSearchShortcut(event) {
  if (!sessionSearchShortcut(event) || !requestSessionSearch()) return false;
  event.preventDefault();
  return true;
}

function requestCurrentSessionFindNavigation(direction) {
  if (modalIsOpen() || !currentSessionFindController.open) return false;
  currentSessionFindController.move(direction === -1 ? -1 : 1);
  return true;
}

function handleCurrentSessionFindNavigationShortcut(event) {
  const direction = currentSessionFindNavigationShortcut(event);
  if (direction === null || !requestCurrentSessionFindNavigation(direction)) return false;
  event.preventDefault();
  return true;
}

function requestCurrentSessionFind() {
  if (modalIsOpen() || !currentSessionFindController.available) return false;
  currentSessionFindController.show().catch(() => {});
  return true;
}

function handleCurrentSessionFindShortcut(event) {
  if (!currentSessionFindShortcut(event)) return false;
  event.preventDefault();
  requestCurrentSessionFind();
  return true;
}

function automaticComposerFocusEnabled() {
  return window.matchMedia?.("(pointer: fine)").matches !== false;
}

function syncComposerFocus(state = composerState?.dataset.state) {
  if (!automaticComposerFocusEnabled() || modalIsOpen()) return;
  if (document.activeElement?.matches?.('[data-tool-output-body][role="region"]')) return;

  const agentBusy = ["running", "sending"].includes(state);
  if (!agentBusy && !conversationController.nearBottom()) return;

  const target = agentBusy ? conversationScroll : promptTextarea;
  target?.focus({ preventScroll: true });
}

function desktopConversationFocusEnabled() {
  return window.matchMedia?.("(max-width: 760px)").matches !== true;
}

function toggleConversationPromptFocus(event, nextElement) {
  if (!nextElement || !desktopConversationFocusEnabled()) return false;
  event.preventDefault();
  nextElement.focus({ preventScroll: true });
  return true;
}

function updatePromptPlaceholder() {
  if (!promptTextarea) return;
  if (composerState?.dataset.state === "running") {
    promptTextarea.placeholder = "Send follow-up…";
    return;
  }
  if (promptTextarea.disabled) {
    promptTextarea.placeholder = "Sending…";
    return;
  }
  promptTextarea.placeholder = window.matchMedia?.("(max-width: 760px)").matches ? "Ask Pi…" : "Ask Pi… Enter to send, Shift+Enter for newline.";
}

function setStatusItem(key, label, value) {
  if (!sessionStatusBar || value === null || value === undefined || value === "") return;

  let item = sessionStatusBar.querySelector(`[data-status-key="${key}"]`);
  if (!item) {
    item = document.createElement(key === "model" ? "button" : "span");
    item.className = `session-status-item${key === "model" ? " model-settings-chip" : ""}`;
    item.dataset.statusKey = key;
    if (key === "model") {
      item.type = "button";
      item.dataset.modalOpen = "model-settings-modal";
      item.setAttribute("aria-label", "Open model and thinking settings");
      item.disabled = ["running", "sending"].includes(composerState?.dataset.state);
    }
    const labelElement = document.createElement("span");
    labelElement.className = "session-status-label";
    labelElement.textContent = label;
    const valueElement = document.createElement("span");
    valueElement.className = "session-status-value";
    item.append(labelElement, " ", valueElement);
    sessionStatusBar.append(item);
  }

  item.querySelector(".session-status-value").textContent = value;
}

function removeStatusItem(key) {
  sessionStatusBar?.querySelector(`[data-status-key="${key}"]`)?.remove();
}

function renderModelStatus() {
  if (!liveStatusModel) {
    removeStatusItem("thinking");
    return;
  }

  setStatusItem("model", "Model", [liveStatusModel, liveStatusThinking ? `(${liveStatusThinking})` : null].filter(Boolean).join(" "));
  removeStatusItem("thinking");
}

function selectedSettingsModel() {
  return modelSettingsModels.find((model) => modelSettingsKey(model) === modelSettingsSelectedKey) || null;
}

function setModelSettingsStatus(message, error = false) {
  const status = document.querySelector("[data-model-settings-status]");
  if (!status) return;
  status.textContent = message;
  status.classList.toggle("is-error", error);
}

function renderThinkingOptions(model, requestedLevel = null) {
  const container = document.querySelector("[data-thinking-levels]");
  const fieldset = document.querySelector("[data-thinking-options]");
  if (!container || !fieldset || !model) return;
  const level = selectedThinkingLevel(model, requestedLevel || modelSettingsCurrentThinking);
  container.replaceChildren();
  supportedThinkingLevels(model).forEach((thinkingLevel) => {
    const label = document.createElement("label");
    label.className = "thinking-option";
    const radio = document.createElement("input");
    radio.type = "radio";
    radio.name = "thinking";
    radio.value = thinkingLevel;
    radio.checked = thinkingLevel === level;
    label.append(radio, thinkingLevel);
    container.append(label);
  });
  fieldset.hidden = false;
}

function modelMetadata(model) {
  const metadata = [];
  if (model.contextWindow) metadata.push(`${compactNumber(model.contextWindow)} context`);
  if (model.maxTokens) metadata.push(`${compactNumber(model.maxTokens)} max output`);
  if (Array.isArray(model.input) && model.input.includes("image")) metadata.push("images");
  metadata.push(model.reasoning ? "reasoning" : "no reasoning");
  return metadata.join(" · ");
}

function renderModelSettingsModels() {
  const list = document.querySelector("[data-model-list]");
  const search = document.querySelector("[data-model-search]")?.value.trim().toLowerCase() || "";
  if (!list) return;
  list.replaceChildren();
  const matches = modelSettingsModels.filter((model) => [model.provider, model.id, model.name].some((value) => String(value || "").toLowerCase().includes(search)));
  const providers = new Map();
  matches.forEach((model) => {
    const models = providers.get(model.provider || "Other") || [];
    models.push(model);
    providers.set(model.provider || "Other", models);
  });
  providers.forEach((models, provider) => {
    const group = document.createElement("section");
    group.className = "model-provider-group";
    const heading = document.createElement("h3");
    heading.className = "model-provider-heading";
    heading.textContent = provider;
    group.append(heading);
    models.forEach((model) => {
      const label = document.createElement("label");
      label.className = "model-option";
      const radio = document.createElement("input");
      radio.type = "radio";
      radio.name = "model";
      radio.value = modelSettingsKey(model);
      radio.checked = radio.value === modelSettingsSelectedKey;
      radio.addEventListener("change", () => {
        modelSettingsSelectedKey = radio.value;
        renderThinkingOptions(model);
      });
      const details = document.createElement("span");
      details.className = "model-option-details";
      const id = document.createElement("span");
      id.className = "model-option-id";
      id.textContent = model.id || "Unknown model";
      details.append(id);
      if (model.name && model.name !== model.id) {
        const name = document.createElement("span");
        name.className = "model-option-name";
        name.textContent = model.name;
        details.append(name);
      }
      const metadata = document.createElement("span");
      metadata.className = "model-option-meta";
      metadata.textContent = modelMetadata(model);
      details.append(metadata);
      label.append(radio, details);
      if (modelSettingsKey(model) === modelSettingsKey(modelSettingsCurrentModel || {})) {
        const badge = document.createElement("span");
        badge.className = "model-current-badge";
        badge.textContent = "Current";
        label.append(badge);
      }
      group.append(label);
    });
    list.append(group);
  });
  setModelSettingsStatus(matches.length ? `${matches.length} model${matches.length === 1 ? "" : "s"}` : "No matching models.");
}

async function loadModelSettings(modal, operation) {
  const sessionPath = currentSessionPath();
  const apply = modal?.querySelector("[data-model-settings-apply]");
  const list = modal?.querySelector("[data-model-list]");
  if (!modal || !sessionPath) return;
  if (list) list.replaceChildren();
  if (apply) apply.disabled = true;
  setModelSettingsStatus("Loading models…");
  try {
    const response = await fetch(`/sessions/model_settings?session=${encodeURIComponent(sessionPath)}`, { headers: { "Accept": "application/json" } });
    const payload = await response.json().catch(() => null);
    if (!response.ok || !payload) throw new Error(payload?.error || "Could not load models.");
    if (operation !== modelSettingsOperationGeneration || modal.hidden || sessionPath !== currentSessionPath()) return;
    modelSettingsModels = Array.isArray(payload.models) ? payload.models : [];
    modelSettingsCurrentModel = payload.state?.model || null;
    modelSettingsCurrentThinking = payload.state?.thinkingLevel || "off";
    modelSettingsSelectedKey = modelSettingsKey(modelSettingsCurrentModel || {});
    if (!modelSettingsModels.some((model) => modelSettingsKey(model) === modelSettingsSelectedKey)) {
      modelSettingsSelectedKey = modelSettingsModels[0] ? modelSettingsKey(modelSettingsModels[0]) : null;
    }
    renderModelSettingsModels();
    renderThinkingOptions(selectedSettingsModel());
    if (apply) apply.disabled = !selectedSettingsModel() || ["running", "sending"].includes(composerState?.dataset.state);
  } catch (error) {
    if (operation === modelSettingsOperationGeneration && !modal.hidden && sessionPath === currentSessionPath()) {
      setModelSettingsStatus(error.message || "Could not load models.", true);
    }
  }
}

function openModelSettingsModal() {
  if (["running", "sending"].includes(composerState?.dataset.state)) return false;
  const modal = document.querySelector('[data-modal="model-settings-modal"]');
  const search = modal?.querySelector("[data-model-search]");
  if (search) search.value = "";
  const operation = ++modelSettingsOperationGeneration;
  openModal(modal);
  loadModelSettings(modal, operation).catch(() => {});
  return !!modal;
}

async function applyModelSettings(form) {
  const modal = form.closest("[data-modal]");
  const sessionPath = currentSessionPath();
  const operation = ++modelSettingsOperationGeneration;
  const model = selectedSettingsModel();
  const thinking = form.querySelector('input[name="thinking"]:checked')?.value;
  const apply = form.querySelector("[data-model-settings-apply]");
  if (!model || !thinking || !sessionPath) return;
  const formData = new FormData();
  formData.set("session", sessionPath);
  formData.set("provider", model.provider || "");
  formData.set("model", model.id || "");
  formData.set("thinking", thinking);
  if (apply) apply.disabled = true;
  setModelSettingsStatus("Applying settings…");
  try {
    const response = await fetch("/sessions/model_settings", { method: "POST", body: formData, headers: { "Accept": "application/json" } });
    const payload = await response.json().catch(() => null);
    if (!response.ok) throw new Error(payload?.error || "Could not apply model settings.");
    if (operation !== modelSettingsOperationGeneration || modal.hidden || sessionPath !== currentSessionPath()) return;
    const effectiveModel = payload?.model;
    liveStatusModel = [effectiveModel?.provider, effectiveModel?.id].filter(Boolean).join("/");
    liveStatusThinking = payload?.thinking;
    renderModelStatus();
    closeModal(modal);
  } catch (error) {
    if (operation === modelSettingsOperationGeneration && !modal.hidden && sessionPath === currentSessionPath()) {
      setModelSettingsStatus(error.message || "Could not apply model settings.", true);
      if (apply) apply.disabled = false;
    }
  }
}

function updateStatusFromMessage(message) {
  const usage = message?.usage;
  if (!usage) return;

  const tokens = usage.totalTokens || usage.total_tokens || usage.tokens;
  if (tokens) setStatusItem("ctx", "CTX", compactNumber(tokens));
  if (message.provider || message.model) {
    liveStatusModel = [message.provider, message.model].filter(Boolean).join("/");
    renderModelStatus();
  }
}

function desktopNotificationAvailable() {
  return Boolean(window.gripiElectron?.showNotification);
}

function notificationAvailable() {
  return desktopNotificationAvailable() || ("Notification" in window && "serviceWorker" in navigator);
}

function notificationsDisabled() {
  return localStorage.getItem("gripi:notifications-disabled") === "true";
}

function notificationsEnabled() {
  return !notificationsDisabled() && (desktopNotificationAvailable() || (("Notification" in window) && Notification.permission === "granted"));
}

function notificationToggleState() {
  if (notificationsDisabled()) return { name: "off", label: "Off", title: "Notifications off — click to enable" };
  if (notificationsEnabled()) return { name: "enabled", label: "On", title: "Notifications on — click to disable" };
  if (!desktopNotificationAvailable() && ("Notification" in window) && Notification.permission === "denied") return { name: "blocked", label: "Blocked", title: "Notifications blocked — click for setup help" };
  return { name: "enable", label: "Enable", title: "Enable notifications" };
}

function updateNotificationToggle() {
  const toggle = document.querySelector("[data-notification-toggle]");
  if (!toggle) return;

  const state = notificationToggleState();
  toggle.classList.toggle("is-enabled", state.name === "enabled");
  toggle.classList.toggle("is-disabled", state.name === "off" || state.name === "enable");
  toggle.classList.toggle("is-blocked", state.name === "blocked");
  toggle.title = state.title;
  toggle.setAttribute("aria-label", state.title);
  const stateLabel = toggle.querySelector("[data-notification-toggle-state]");
  if (stateLabel) stateLabel.textContent = state.label;
}

async function toggleNotifications() {
  if (notificationsEnabled()) {
    localStorage.setItem("gripi:notifications-disabled", "true");
    updateNotificationToggle();
    return;
  }

  localStorage.removeItem("gripi:notifications-disabled");
  if (desktopNotificationAvailable()) {
    updateNotificationToggle();
    return;
  }

  if (!notificationAvailable() || Notification.permission === "denied") {
    window.location.href = "/notification-test";
    return;
  }

  if (Notification.permission === "default") await Notification.requestPermission();
  updateNotificationToggle();
}

async function ensureNotificationWorker() {
  if (desktopNotificationAvailable() || !notificationAvailable() || Notification.permission !== "granted") return null;
  notificationRegistration ||= await navigator.serviceWorker.register("/service-worker.js");
  await navigator.serviceWorker.ready;
  return notificationRegistration;
}

async function showGripiNotification(title, body, url, tag) {
  if (notificationsDisabled()) return;

  if (desktopNotificationAvailable()) {
    await window.gripiElectron.showNotification({ type: "gripi-notification", title, body, url, tag });
    return;
  }

  const worker = await ensureNotificationWorker();
  if (!worker) return;
  if (worker.active) {
    worker.active.postMessage({ type: "gripi-notification", title, body, url, tag });
  } else {
    await worker.showNotification(title, { body, tag, renotify: true, icon: "/app-icon.svg", badge: "/app-icon.svg", data: { url } });
  }
}

function sessionIsActivelyViewed(sessionPath) {
  return sessionPath && sessionPath === currentSessionPath() && !document.hidden && document.hasFocus();
}

function finalAssistantReplyKey(sessionPath, event) {
  const message = liveMessageParser.eventMessage(event);
  const text = liveMessageParser.messageText(message);
  return [sessionPath, message?.id || message?.messageId || event.id || event.messageId || lastEventSeq, stableTextHash(text)].join(":");
}

function notifyFinalAssistantReply(event) {
  const message = liveMessageParser.eventMessage(event);
  const roleName = liveMessageParser.liveEventRole(event, message);
  if (roleName !== "assistant" || event.type !== "message_end") return;
  if (!liveMessageParser.eventHasFinalAssistantText(event)) return;

  const sessionPath = currentSessionPath();
  if (!sessionPath) return;
  if (sessionIsActivelyViewed(sessionPath)) return;

  const key = finalAssistantReplyKey(sessionPath, event);
  if (notifiedFinalReplyKeys.has(key)) return;
  notifiedFinalReplyKeys.add(key);
  const name = document.querySelector(".session-header-name")?.textContent.trim() || "current session";
  const body = notificationReplyPreview(liveMessageParser.finalAssistantReplyText(message));
  showGripiNotification(name, body, window.location.href, `gripi-final-reply:${sessionPath}`).catch(() => {});
}

function updateStatusFromEvent(event) {
  if (event.type === "model_change" || event.type === "model_select") {
    const provider = event.provider || event.model?.provider;
    const model = event.modelId || event.model?.id || event.model;
    if (provider || model) {
      liveStatusModel = [provider, model].filter(Boolean).join("/");
      renderModelStatus();
    }
  }
  if (["thinking_level_change", "thinking_level_changed", "thinking_level_select"].includes(event.type)) {
    liveStatusThinking = event.thinkingLevel || event.level;
    renderModelStatus();
  }
  updateStatusFromMessage(event.message);
}

async function refreshSessionStatus(generation = sessionViewGeneration) {
  const statusBar = sessionStatusBar;
  const statusUrl = statusBar?.dataset.statusUrl;
  if (!statusBar || !statusUrl) return;

  const response = await fetch(statusUrl);
  if (!response.ok || generation !== sessionViewGeneration || statusBar !== sessionStatusBar) return;
  const status = await response.json();
  if (generation !== sessionViewGeneration || statusBar !== sessionStatusBar) return;
  setStatusItem("ctx", "CTX", status.context);
  liveStatusModel = status.model;
  liveStatusThinking = status.thinking;
  renderModelStatus();
}

function updateWaitingForOutputStatus() {
  if (!composerState || composerState.dataset.state !== "running" || !waitingForOutputSince) return;
  if (Date.now() <= escapeStopConfirmationExpiresAt) {
    composerState.textContent = "Press ESC again to stop current task";
    return;
  }

  const elapsed = Date.now() - waitingForOutputSince;
  composerState.textContent = `${waitingForOutputLabel} ${formatWaitDuration(elapsed)}`;
}

function startWaitingForOutput(since = Date.now()) {
  waitingForOutputSince = since || Date.now();
  clearInterval(waitingForOutputTimer);
  updateWaitingForOutputStatus();
  waitingForOutputTimer = setInterval(updateWaitingForOutputStatus, 1000);
}

function stopWaitingForOutput() {
  waitingForOutputSince = null;
  clearInterval(waitingForOutputTimer);
  waitingForOutputTimer = null;
}

function sessionSyncBlocked() {
  return ["external_follow", "conflict"].includes(liveOutput?.dataset.sessionSyncMode);
}

function setComposerState(state, label = "", { since = null, focus = true } = {}) {
  const previousState = composerState?.dataset.state;
  if (state === "running") waitingForOutputLabel = label || "Pi is running…";
  if (state === "running" && (since || !waitingForOutputSince)) startWaitingForOutput(since || Date.now());
  if (state !== "running") escapeStopConfirmationExpiresAt = 0;
  if (!["running", "sending"].includes(state)) stopWaitingForOutput();
  if (composerState) {
    composerState.dataset.state = state;
    composerState.textContent = ["running", "sending", "error"].includes(state) ? label : "";
    if (state === "running") updateWaitingForOutputStatus();
  }
  const agentBusy = ["running", "sending"].includes(state);
  const submitting = state === "sending";
  if (abortButton) abortButton.disabled = !agentBusy;
  if (sendButton) {
    sendButton.hidden = submitting;
    sendButton.disabled = sessionSyncBlocked();
    sendButton.textContent = state === "running" ? "Queue" : state === "sending" ? "Sending…" : "Send";
    sendButton.setAttribute("aria-label", state === "running" ? "Send follow-up" : "Send message");
  }
  if (composerStopButton) {
    composerStopButton.hidden = !agentBusy;
    composerStopButton.disabled = !agentBusy;
    composerStopButton.classList.toggle("is-visible", agentBusy);
  }
  if (promptTextarea) promptTextarea.disabled = submitting || sessionSyncBlocked();
  if (focus && state !== previousState) syncComposerFocus(state);
  const modelButton = sessionStatusBar?.querySelector('[data-status-key="model"]');
  if (modelButton) modelButton.disabled = agentBusy || sessionSyncBlocked();
  const modelApply = document.querySelector('[data-modal="model-settings-modal"] [data-model-settings-apply]');
  if (modelApply && !document.querySelector('[data-modal="model-settings-modal"]')?.hidden) modelApply.disabled = agentBusy || !selectedSettingsModel();
  if (composerState && state === "running" && previousState !== "running") {
    resetEventPollBackoff();
    scheduleNextEventPoll(0);
    sidebarController.requestRefresh();
  }
  const attachmentsDisabled = submitting || sessionSyncBlocked();
  if (imageInput) imageInput.disabled = attachmentsDisabled;
  if (attachButton) {
    attachButton.classList.toggle("is-disabled", attachmentsDisabled);
    attachButton.setAttribute("aria-disabled", attachmentsDisabled ? "true" : "false");
  }
  updatePromptPlaceholder();
  updateCommandListForPrompt();
}

function resizePromptTextarea() {
  if (!promptTextarea) return;

  promptTextarea.style.height = "auto";
  const maxHeight = parseFloat(getComputedStyle(promptTextarea).maxHeight);
  const hasMaxHeight = Number.isFinite(maxHeight) && maxHeight > 0;
  const nextHeight = hasMaxHeight ? Math.min(promptTextarea.scrollHeight, maxHeight) : promptTextarea.scrollHeight;

  promptTextarea.style.height = `${nextHeight}px`;
  promptTextarea.style.overflowY = hasMaxHeight && promptTextarea.scrollHeight > maxHeight ? "auto" : "hidden";
}

function cycleThinkingShortcut(event) {
  return event.key === "Tab" && event.shiftKey && !event.ctrlKey && !event.metaKey && !event.altKey &&
    document.activeElement === promptTextarea && composerState?.dataset.state === "idle" && !modalIsOpen();
}

async function cycleThinking() {
  const sessionPath = currentSessionPath();
  const generation = sessionViewGeneration;
  if (!sessionPath || thinkingCyclePending) return;
  thinkingCyclePending = true;
  const formData = new FormData();
  formData.set("session", sessionPath);
  try {
    const response = await fetch("/sessions/cycle_thinking", { method: "POST", body: formData, headers: { "Accept": "application/json" } });
    const payload = await response.json().catch(() => null);
    if (!response.ok) throw new Error(payload?.error || "Could not change thinking level.");
    if (generation !== sessionViewGeneration || sessionPath !== currentSessionPath()) return;
    if (payload?.thinking) {
      liveStatusThinking = payload.thinking;
      renderModelStatus();
    }
  } catch (_error) {
  } finally {
    thinkingCyclePending = false;
  }
}

function appendSessionNameFeedback(payload) {
  if (payload.current) return;
  const backtickRuns = String(payload.name).match(/`+/g) || [];
  const delimiter = "`".repeat(Math.max(1, ...backtickRuns.map((run) => run.length + 1)));
  liveMessageRenderer.appendMessage("status", `Session renamed to: ${delimiter}${payload.name}${delimiter}`, true, true, new Date(), { markdown: true });
}

function updateSessionHeaderName(name) {
  if (!name) return;
  const headerName = document.querySelector(".session-header-name");
  if (!headerName) return;
  headerName.textContent = name;
  headerName.title = name;
  const title = headerName.closest(".session-header-title");
  const project = title?.querySelector(".session-header-project-label")?.textContent.trim();
  if (title) title.title = project ? `${name} · ${project}` : name;
  document.title = `${name} · GRIPi`;
}

function renderAttachments() {
  if (!attachmentTray) return;
  attachmentTray.replaceChildren();
  attachmentTray.classList.toggle("has-attachments", pendingImages.length > 0);

  pendingImages.forEach((entry, index) => {
    const wrapper = document.createElement("span");
    wrapper.className = "attachment";

    const image = document.createElement("img");
    image.src = entry.url;
    image.alt = "Attached image preview";

    const label = document.createElement("span");
    label.textContent = entry.file.name || `Image ${index + 1}`;

    const remove = document.createElement("button");
    remove.type = "button";
    remove.textContent = "Remove";
    remove.addEventListener("click", () => {
      URL.revokeObjectURL(entry.url);
      pendingImages.splice(index, 1);
      renderAttachments();
    });

    wrapper.append(image, label, remove);
    attachmentTray.append(wrapper);
  });
}

function addImageFiles(files, { restore = false } = {}) {
  if (!restore && (promptTextarea?.disabled || composerState?.dataset.state === "running")) return false;

  const imageFiles = [...files].filter((file) => file.type.startsWith("image/"));
  if (imageFiles.length === 0) return false;

  imageFiles.forEach((file) => {
    pendingImages.push({ file, url: URL.createObjectURL(file) });
  });
  renderAttachments();
  return true;
}

function clearAttachments() {
  pendingImages.forEach((entry) => URL.revokeObjectURL(entry.url));
  pendingImages = [];
  renderAttachments();
}

function showStatus(_text, _forceScroll = false) {}

function eventTimeMilliseconds(event) {
  const value = eventTimestamp(event);
  const timestamp = typeof value === "number" ? value : Date.parse(value);
  return Number.isFinite(timestamp) ? timestamp : Date.now();
}

function renderErrorEvent(event) {
  const errorText = eventErrorText(event);
  if (!errorText) return false;
  liveErrorSeen = true;
  liveMessageRenderer.appendMessage("error", errorText, true, true, eventTimestamp(event));
  showStatus(errorText, true);
  setComposerState("error", errorText);
  return true;
}

function renderEvent(event) {
  if (event.type === "agent_start") {
    liveAgentRunning = true;
    liveBusySince = eventTimeMilliseconds(event);
    liveErrorSeen = false;
    setComposerState("running", "Pi is running…", { since: liveBusySince });
    showStatus("Pi is thinking…");
    return;
  }

  if (event.type === "turn_start") {
    liveBusySince ||= eventTimeMilliseconds(event);
    liveErrorSeen = false;
    setComposerState("running", "Pi is running…", { since: liveBusySince });
    showStatus("Pi is thinking…");
    return;
  }

  if (["message_start", "message_update", "message_end"].includes(event.type)) {
    const outcome = liveMessageRenderer.renderMessageEvent(event);
    if (outcome.finalAssistantEnded) markCurrentSessionRead();
    notifyFinalAssistantReply(event);
    return;
  }

  if (["tool_execution_start", "tool_execution_update", "tool_execution_end"].includes(event.type)) {
    liveMessageRenderer.clearLiveAssistantStreaming();
    liveMessageRenderer.renderToolExecutionEvent(event);
    return;
  }

  if (event.type === "compaction") {
    liveMessageRenderer.renderCompactionEvent(event);
    showStatus("Compaction finished");
    if (liveAgentRunning) setComposerState("running", "Pi is running…", { since: liveBusySince });
    else {
      liveBusySince = null;
      setComposerState("done", "Done");
    }
    refreshSessionStatus().catch(() => {});
    sidebarController.refresh().catch(() => {});
    return;
  }

  if (event.type === "extension_ui_request" && event.method === "set_editor_text") {
    if (promptTextarea) {
      promptTextarea.value = event.text || "";
      resizePromptTextarea();
    }
    setComposerState("idle", "", { focus: false });
    syncComposerFocus();
    showStatus("Tree position selected");
    return;
  }

  if (["custom", "custom_message", "session_info", "session_info_changed", "queue_update", "compaction_start", "compaction_end"].includes(event.type)) {
    updateSessionHeaderName(sessionNameFromEvent(event));
    if (["session_info", "session_info_changed"].includes(event.type)) sidebarController.refresh().catch(() => {});
    showStatus(eventStatusText(event));
    if (event.type === "compaction_start") {
      liveMessageRenderer.resetLiveCompactionTracking();
      liveMessageRenderer.removePendingCompactionMessage();
      liveMessageRenderer.appendPendingCompactionMessage(eventTimestamp(event));
      setComposerState("running", "Compacting…", { since: eventTimeMilliseconds(event) });
    }
    if (event.type === "compaction_end") {
      liveMessageRenderer.removePendingCompactionMessage();
      if (!event.aborted && !liveMessageRenderer.liveCompactionRendered) liveMessageRenderer.renderCompactionEvent(event);
      if (liveAgentRunning) setComposerState("running", "Pi is running…", { since: liveBusySince });
      else {
        liveBusySince = null;
        setComposerState("done", event.aborted ? "Compaction aborted" : "Done");
      }
      if (!event.aborted) refreshSessionStatus().catch(() => {});
      sidebarController.refresh().catch(() => {});
    }
    return;
  }

  if (event.type === "turn_end") {
    if (!liveAgentRunning) liveBusySince = null;
    if (!liveErrorSeen) {
      if (liveMessageRenderer.liveAssistantSeen) showStatus("Done");
      if (!liveAgentRunning) setComposerState("done", "Done");
    }
    liveMessageRenderer.clearLiveAssistantStreaming();
    liveMessageRenderer.resetLiveAssistantTracking();
    return;
  }

  if (event.type === "agent_settled") {
    liveAgentRunning = false;
    liveBusySince = null;
    if (renderErrorEvent(event)) {
      liveMessageRenderer.clearLiveAssistantStreaming();
      liveMessageRenderer.resetLiveAssistantTracking();
      return;
    }
    if (!liveErrorSeen) {
      if (liveMessageRenderer.liveAssistantSeen) showStatus("Done");
      setComposerState("done", "Done");
    }
    liveMessageRenderer.clearLiveAssistantStreaming();
    liveMessageRenderer.resetLiveAssistantTracking();
    return;
  }

  renderErrorEvent(event);
}

function nextEventPollDelay(failed = false) {
  const delay = eventPollingDelay(document.hidden, composerState?.dataset.state, emptyEventPollCount, failed);
  return !document.hidden && sessionSyncBlocked() ? Math.min(delay, 1000) : delay;
}

function resetEventPollBackoff() {
  emptyEventPollCount = 0;
}

function resetEventCursor() {
  lastEventSeq = Number(liveOutput?.dataset.eventsAfter || 0);
}

function scheduleNextEventPoll(delay = nextEventPollDelay()) {
  if (!liveOutput) return;
  clearTimeout(eventPollTimer);
  eventPollTimer = null;
  if (modalIsOpen()) return;
  eventPollTimer = setTimeout(() => pollEvents().catch(() => {}), delay);
}

function showReconnectBanner() {
  reconnectBanner?.classList.add("is-visible");
}

function hideReconnectBanner() {
  reconnectBanner?.classList.remove("is-visible");
}

function abortEventPoll() {
  if (eventPollAbortController) {
    eventPollAbortController.piSuppressedAbort = true;
    eventPollAbortController.abort();
  }
  eventPollAbortController = null;
  eventPollInFlight = false;
}

function composerDraftStorageKey(session = promptSessionInput?.value || "") {
  return session ? `gripi:composer-draft:${session}` : null;
}

function loadStoredComposerDraft() {
  const key = composerDraftStorageKey();
  if (!key || !promptTextarea) return;

  try {
    const message = localStorage.getItem(key);
    if (message === null || promptTextarea.value) return;
    promptTextarea.value = message;
    resizePromptTextarea();
  } catch (_error) {
  }
}

function persistStoredComposerDraft() {
  const key = composerDraftStorageKey();
  if (!key || !promptTextarea) return;

  try {
    if (promptTextarea.value) localStorage.setItem(key, promptTextarea.value);
    else localStorage.removeItem(key);
  } catch (_error) {
  }
}

function clearStoredComposerDraft(session = promptSessionInput?.value || "") {
  const key = composerDraftStorageKey(session);
  if (!key) return;

  try {
    localStorage.removeItem(key);
  } catch (_error) {
  }
}

function composerDraft() {
  return {
    session: promptSessionInput?.value || "",
    message: promptTextarea?.value || "",
    images: pendingImages.map((entry) => entry.file)
  };
}

function restoreComposerDraft(draft) {
  if (!draft || promptSessionInput?.value !== draft.session) return;
  if (promptTextarea && draft.message) {
    promptTextarea.value = draft.message;
    resizePromptTextarea();
    persistStoredComposerDraft();
  }
  if (draft.images.length > 0) addImageFiles(draft.images, { restore: true });
}

async function refreshCurrentSessionPreservingComposer() {
  const draft = composerDraft();
  const refreshed = await switchSession(window.location.href, { push: false, focus: false, preserveScroll: true });
  if (refreshed) restoreComposerDraft(draft);
  return refreshed;
}

async function reconnectSession() {
  hideReconnectBanner();
  await refreshCurrentSessionPreservingComposer();
}

async function refreshStaleSessionAfterResume(hiddenDuration = 0) {
  if (!liveOutput || document.hidden) return false;
  if (staleSessionRefreshInFlight) return true;

  const pollingGap = Date.now() - lastEventPollSuccessAt;
  if (hiddenDuration < STALE_SESSION_REFRESH_AFTER_MS && pollingGap < STALE_SESSION_REFRESH_AFTER_MS) return false;

  staleSessionRefreshInFlight = true;
  try {
    return await refreshCurrentSessionPreservingComposer();
  } finally {
    staleSessionRefreshInFlight = false;
  }
}

async function resumeEventPolling(hiddenDuration = 0) {
  if (!liveOutput) return;

  const resumeStartedAt = Date.now();
  clearTimeout(eventPollTimer);
  clearTimeout(eventPollResumeTimer);
  abortEventPoll();
  resetEventPollBackoff();
  hideReconnectBanner();
  if (await refreshStaleSessionAfterResume(hiddenDuration)) return;
  scheduleNextEventPoll(0);
  eventPollResumeTimer = setTimeout(() => {
    if (!document.hidden && lastEventPollSuccessAt < resumeStartedAt) showReconnectBanner();
  }, 5000);
}

function sessionSyncRefreshRequired(sync) {
  if (!sync || !liveOutput) return false;

  const renderedMode = liveOutput.dataset.sessionSyncMode;
  const incomingBlocked = ["external_follow", "conflict"].includes(sync.mode);
  const renderedBlocked = ["external_follow", "conflict"].includes(renderedMode);
  return (incomingBlocked && (sync.mode !== renderedMode || sync.revision !== liveOutput.dataset.sessionSyncRevision)) ||
    (renderedBlocked && !incomingBlocked) ||
    (renderedBlocked && composerState?.dataset.state === "running" && sync.gateway_busy === false) ||
    (renderedMode === "managed" && sync.mode === "available");
}

async function pollEvents() {
  if (!liveOutput) return;
  if (modalIsOpen()) return;
  if (eventPollInFlight) return;

  const generation = sessionViewGeneration;
  const controller = new AbortController();
  const pollTimeout = setTimeout(() => controller.abort(), 12000);
  let pollSucceeded = false;
  eventPollInFlight = true;
  eventPollAbortController = controller;
  try {
    const eventsUrl = new URL(liveOutput.dataset.eventsUrl, window.location.origin);
    eventsUrl.searchParams.set("after", lastEventSeq);
    const response = await fetch(eventsUrl, { signal: controller.signal });
    if (!response.ok || generation !== sessionViewGeneration) return;

    const payload = await response.json();
    if (generation !== sessionViewGeneration) return;
    lastEventPollSuccessAt = Date.now();
    pollSucceeded = true;
    hideReconnectBanner();
    if (sessionSyncRefreshRequired(payload.session_sync)) {
      await refreshCurrentSessionPreservingComposer();
      return;
    }
    if (payload.missed) {
      await refreshCurrentSessionPreservingComposer();
      return;
    }
    if (Number.isInteger(payload.last_seq)) {
      lastEventSeq = payload.last_seq;
    }
    emptyEventPollCount = payload.events.length > 0 ? 0 : emptyEventPollCount + 1;
    if (payload.events.length > 0 && composerState?.dataset.state === "running" && !waitingForOutputSince) startWaitingForOutput();
    updateWaitingForOutputStatus();
    payload.events.forEach((event) => {
      updateStatusFromEvent(event);
      renderEvent(event);
    });
    if (payload.events.length > 0) refreshSessionStatus(generation).catch(() => {});
  } catch (_error) {
    if (!controller.piSuppressedAbort && generation === sessionViewGeneration && !document.hidden) showReconnectBanner();
  } finally {
    clearTimeout(pollTimeout);
    if (eventPollAbortController === controller) eventPollAbortController = null;
    if (generation === sessionViewGeneration) {
      eventPollInFlight = false;
      scheduleNextEventPoll(nextEventPollDelay(!pollSucceeded));
    }
  }
}

async function submitPrompt(event) {
  event.preventDefault();

  if (promptTextarea?.disabled) return;

  const followUp = composerState?.dataset.state === "running";
  const previousWaitingForOutputSince = waitingForOutputSince;

  const generation = sessionViewGeneration;
  const switchGeneration = sessionSwitchGeneration;
  const submittedSession = promptSessionInput?.value;
  const submittedViewChanged = () => generation !== sessionViewGeneration || switchGeneration !== sessionSwitchGeneration || submittedSession !== promptSessionInput?.value;
  const stopHandlingChangedSubmittedView = () => {
    if (!submittedViewChanged()) return false;

    sidebarController.requestRefresh();
    return true;
  };
  const message = promptTextarea.value.trim();
  const submittedImageFiles = pendingImages.map((entry) => entry.file);
  if (!message && submittedImageFiles.length === 0) return;

  const formData = new FormData(promptForm);
  addSessionViewFormParams(formData);
  formData.set("message", message);
  submittedImageFiles.forEach((file) => formData.append("images[]", file, file.name || "image"));
  if (followUp) formData.set("streaming_behavior", "follow_up");

  const nameCommand = followUp ? null : sessionNameSlashCommand(message);
  const compactCommand = followUp ? null : sessionCompactSlashCommand(message);
  const forkCommand = followUp ? null : sessionForkSlashCommand(message);
  const treeCommand = followUp ? null : sessionTreeSlashCommand(message);
  const cloneCommand = followUp ? null : sessionCloneSlashCommand(message);
  const newCommand = followUp ? null : sessionNewSlashCommand(message);
  const modelCommand = followUp ? null : sessionModelSlashCommand(message);
  if (!nameCommand && !compactCommand && !forkCommand && !treeCommand && !cloneCommand && !newCommand && !modelCommand) {
    if (!followUp) {
      liveMessageRenderer.resetLiveAssistantTracking();
      document.querySelectorAll(".tree-position-banner").forEach((banner) => banner.remove());
      const optimisticImages = pendingImages.map((entry) => ({ src: URL.createObjectURL(entry.file), alt: entry.file.name || "Attached image" }));
      liveMessageRenderer.appendMessage("user", message || `[${imageAttachmentLabel(submittedImageFiles.length)}]`, true, true, new Date(), { optimistic: true, optimisticText: message, images: optimisticImages });
    }
    resetEventPollBackoff();
    scheduleNextEventPoll(0);
  } else if (compactCommand) {
    liveMessageRenderer.resetLiveAssistantTracking();
    liveMessageRenderer.resetLiveCompactionTracking();
    resetEventPollBackoff();
    scheduleNextEventPoll(0);
    liveMessageRenderer.appendPendingCompactionMessage(new Date());
    sidebarController.markSessionCompacting(submittedSession);
  }
  promptTextarea.value = "";
  clearStoredComposerDraft(submittedSession);
  clearAttachments();
  commandList?.classList.remove("is-visible");
  commandList?.removeAttribute("open");
  resetCommandSelection();
  resizePromptTextarea();
  setComposerState("sending", nameCommand ? "Naming…" : compactCommand ? "Compacting…" : cloneCommand ? "Cloning…" : newCommand ? "Starting…" : forkCommand ? "Opening fork…" : treeCommand ? "Opening tree…" : modelCommand ? "Opening model settings…" : followUp ? "Queueing follow-up…" : "Sending…");
  showStatus(nameCommand ? "Setting session name…" : compactCommand ? "Compacting session…" : cloneCommand ? "Cloning session…" : newCommand ? "Starting new session…" : forkCommand ? "Opening fork picker…" : treeCommand ? "Opening session tree…" : modelCommand ? "Opening model settings…" : followUp ? "Queueing follow-up…" : "Sending…", true);
  if (cloneCommand || newCommand) showSessionSwitching();

  const restoreSubmittedComposerInput = () => {
    if (promptTextarea && message) {
      promptTextarea.value = message;
      persistStoredComposerDraft();
    }
    pendingImages = submittedImageFiles.map((file) => ({ file, url: URL.createObjectURL(file) }));
    renderAttachments();
    resizePromptTextarea();
    if (cloneCommand || newCommand) hideSessionSwitching();
  };

  const showPromptFailure = (errorMessage) => {
    restoreSubmittedComposerInput();
    if (followUp) {
      setComposerState("running", "Pi is running…", { since: previousWaitingForOutputSince });
      showStatus(errorMessage, true);
      return;
    }
    liveMessageRenderer.markOptimisticUserMessageFailed(message);
    setComposerState("error", errorMessage);
    showStatus(errorMessage, true);
    liveMessageRenderer.appendMessage("assistant", `Prompt failed to send:\n\n${errorMessage}`, true, true, new Date(), { finalAssistantResponse: true });
  };

  let response;
  try {
    response = await fetch(promptForm.action, { method: "POST", body: formData, headers: { "Accept": "application/json" }, redirect: "manual" });
  } catch (_error) {
    if (stopHandlingChangedSubmittedView()) return;
    if (nameCommand) {
      restoreSubmittedComposerInput();
      setComposerState("error", "Session name could not be changed");
      showStatus("Session name could not be changed", true);
      return;
    }
    showPromptFailure("Prompt failed to send");
    return;
  }
  if (stopHandlingChangedSubmittedView()) return;

  if (!response.ok && response.type !== "opaqueredirect") {
    const payload = await response.json().catch(() => null);
    if (stopHandlingChangedSubmittedView()) return;
    if (cloneCommand && payload?.cancelled) {
      restoreSubmittedComposerInput();
      setComposerState("idle");
      showStatus("Clone cancelled", true);
      return;
    }
    if (nameCommand) {
      restoreSubmittedComposerInput();
      setComposerState("error", payload?.error || "Session name could not be changed");
      showStatus(payload?.error || "Session name could not be changed", true);
      return;
    }
    showPromptFailure(payload?.error || "Prompt failed to send");
  } else if (response.ok) {
    const payload = await response.json().catch(() => null);
    if (stopHandlingChangedSubmittedView()) return;
    if (cloneCommand && payload?.cancelled) {
      restoreSubmittedComposerInput();
      setComposerState("idle");
      showStatus("Clone cancelled", true);
      return;
    }
    if (payload?.command === "name") {
      if (payload.error) {
        restoreSubmittedComposerInput();
        setComposerState("error", payload.error);
        showStatus(payload.error, true);
        return;
      }
      clearStoredComposerDraft(submittedSession);
      if (payload?.session && promptSessionInput && payload.session !== promptSessionInput.value) {
        const switched = await switchSession(payload.redirect || `/?session=${encodeURIComponent(payload.session)}`, { push: true, focus: true });
        if (switched) appendSessionNameFeedback(payload);
        return;
      }
      updateSessionHeaderName(payload.name);
      setComposerState("done", payload.current ? "Named" : "Name set");
      showStatus(payload.current ? `Session name: “${payload.name}”` : eventStatusText({ type: "session_info", name: payload.name }), true);
      appendSessionNameFeedback(payload);
      sidebarController.refresh().catch(() => {});
      return;
    }
    clearStoredComposerDraft(submittedSession);
    if (payload?.command === "compact") {
      sidebarController.refresh().catch(() => {});
      if (composerState?.dataset.state === "sending") setComposerState("running", "Compacting…");
      showStatus("Compaction started", true);
      return;
    }
    if (payload?.command === "fork") {
      setComposerState("idle", "", { focus: false });
      showStatus("Choose a fork point", true);
      openForkSessionModal();
      return;
    }
    if (payload?.command === "tree") {
      setComposerState("idle", "", { focus: false });
      showStatus("Choose a tree entry", true);
      openTreeSessionModal();
      return;
    }
    if (payload?.command === "model") {
      setComposerState("idle", "", { focus: false });
      openModelSettingsModal();
      return;
    }
    if (payload?.session && promptSessionInput && payload.session !== promptSessionInput.value) {
      await switchSession(payload.redirect || `/?session=${encodeURIComponent(payload.session)}`, { push: true, focus: true });
      return;
    }
    if (cloneCommand || newCommand) hideSessionSwitching();
    setComposerState("running", "Pi is running…");
    if (payload?.follow_up) showStatus("Sent to follow-up queue", true);
  } else {
    clearStoredComposerDraft(submittedSession);
    setComposerState("running", "Pi is running…");
  }
  conversationController.scrollToBottom();
}

async function submitAbort(event) {
  event.preventDefault();
  if (!abortForm || abortForm.dataset.submitting === "true") return;

  abortForm.dataset.submitting = "true";
  showSessionSwitching();
  try {
    const response = await fetch(abortForm.action, { method: "POST", body: new FormData(abortForm), headers: { "Accept": "application/json" } });
    if (!response.ok) showStatus("Stop failed", true);
  } catch (_error) {
    showStatus("Stop failed", true);
  } finally {
    delete abortForm.dataset.submitting;
    hideSessionSwitching();
    scheduleNextEventPoll(0);
    sidebarController.refresh().catch(() => {});
  }
}

function confirmOrStopRunningTask(event) {
  if (composerState?.dataset.state !== "running") return false;

  event.preventDefault();
  if (event.repeat) return true;

  const now = Date.now();
  if (now <= escapeStopConfirmationExpiresAt) {
    escapeStopConfirmationExpiresAt = 0;
    if (composerState) composerState.textContent = "Stopping current task…";
    showStatus("Stopping current task…", true);
    abortForm.requestSubmit();
    return true;
  }

  escapeStopConfirmationExpiresAt = now + ESCAPE_STOP_CONFIRMATION_WINDOW_MS;
  updateWaitingForOutputStatus();
  showStatus("Press ESC again to stop current task", true);
  return true;
}

function composingFollowUp() {
  return composerState?.dataset.state === "running";
}

function visibleCommands() {
  if (composingFollowUp()) return [];
  return [...(commandList?.querySelectorAll(".command") || [])].filter((command) => !command.hidden);
}

function updateHighlightedCommand() {
  const commands = visibleCommands();
  if (highlightedCommandIndex >= commands.length) highlightedCommandIndex = commands.length - 1;
  if (highlightedCommandIndex < 0) highlightedCommandIndex = 0;
  commandList?.querySelectorAll(".command").forEach((command) => command.classList.remove("is-highlighted"));
  commands[highlightedCommandIndex]?.classList.add("is-highlighted");
}

function resetCommandSelection() {
  highlightedCommandIndex = 0;
  hideQueuedSlashCommandMessage();
  commandList?.querySelectorAll(".command-list h3").forEach((heading) => { heading.hidden = false; });
  commandList?.querySelectorAll(".command").forEach((command) => {
    command.hidden = false;
    command.classList.remove("is-highlighted");
  });
}

function showQueuedSlashCommandMessage() {
  if (!commandList) return;
  commandList.querySelector("[data-queued-slash-message]")?.removeAttribute("hidden");
  commandList.querySelectorAll(".command-list h3, .command").forEach((element) => { element.hidden = true; });
  highlightedCommandIndex = 0;
}

function hideQueuedSlashCommandMessage() {
  commandList?.querySelector("[data-queued-slash-message]")?.setAttribute("hidden", "");
}

function filterCommandsFromPrompt() {
  if (!commandList || !promptTextarea) return;
  if (composingFollowUp()) return showQueuedSlashCommandMessage();
  hideQueuedSlashCommandMessage();
  commandList.querySelectorAll(".command-list h3").forEach((heading) => { heading.hidden = false; });
  const query = promptTextarea.value.startsWith("/") ? promptTextarea.value.slice(1).trim().toLowerCase() : "";
  commandList.querySelectorAll(".command").forEach((command) => {
    command.hidden = query && !command.dataset.commandText.toLowerCase().includes(query);
  });
  highlightedCommandIndex = 0;
  updateHighlightedCommand();
}

function selectCommand(command) {
  if (composingFollowUp() || !command || !promptTextarea) return false;
  promptTextarea.value = `/${command.dataset.commandName} `;
  commandList?.classList.remove("is-visible");
  commandList?.removeAttribute("open");
  resetCommandSelection();
  resizePromptTextarea();
  promptTextarea.focus();
  return true;
}

function selectHighlightedCommand() {
  return selectCommand(visibleCommands()[highlightedCommandIndex]);
}

function moveHighlightedCommand(direction) {
  const commands = visibleCommands();
  if (commands.length === 0) return;
  highlightedCommandIndex = (highlightedCommandIndex + direction + commands.length) % commands.length;
  updateHighlightedCommand();
  commands[highlightedCommandIndex]?.scrollIntoView({ block: "nearest" });
}

async function ensureCommandsLoaded() {
  const list = commandList;
  const generation = sessionSwitchGeneration;
  if (!list || composingFollowUp() || list.dataset.loaded === "true") return;
  const url = list.dataset.commandsUrl;
  if (!url || list.dataset.loading === "true") return;

  list.dataset.loading = "true";
  try {
    const response = await fetch(url);
    if (!response.ok || commandList !== list || !list.isConnected || generation !== sessionSwitchGeneration || list.dataset.commandsUrl !== url) return;
    const html = await response.text();
    if (commandList !== list || !list.isConnected || generation !== sessionSwitchGeneration || list.dataset.commandsUrl !== url) return;
    list.outerHTML = html;
    commandList = document.getElementById("command-list");
    highlightedCommandIndex = 0;
    if (promptTextarea?.value.startsWith("/")) {
      commandList?.classList.add("is-visible");
      commandList?.setAttribute("open", "");
      filterCommandsFromPrompt();
    }
  } catch (_error) {
  } finally {
    delete list.dataset.loading;
  }
}

function updateCommandListForPrompt() {
  if (!commandList || !promptTextarea) return;

  if (promptTextarea.value.startsWith("/")) {
    commandList.classList.add("is-visible");
    commandList.setAttribute("open", "");
    if (composingFollowUp()) {
      showQueuedSlashCommandMessage();
    } else {
      ensureCommandsLoaded();
      filterCommandsFromPrompt();
    }
  } else {
    commandList.classList.remove("is-visible");
    commandList.removeAttribute("open");
    resetCommandSelection();
  }
}

function recordKeyboardConversationScrollIntent(event) {
  if (editableElement(event.target) || !keyboardScrollKey(event)) return;
  conversationController.recordScrollIntent("keyboard");
}

function bindPageLifetimeControls() {
  document.addEventListener("keydown", recordKeyboardConversationScrollIntent);
}

function bindSessionControls() {
  promptTextarea?.addEventListener("keydown", (event) => {
    if (promptTextarea.value.startsWith("/") && commandList?.classList.contains("is-visible")) {
      if (event.key === "ArrowDown") {
        event.preventDefault();
        moveHighlightedCommand(1);
        return;
      }
      if (event.key === "ArrowUp") {
        event.preventDefault();
        moveHighlightedCommand(-1);
        return;
      }
      if (((event.key === "Enter" && !event.shiftKey) || (event.key === "Tab" && !event.shiftKey)) && visibleCommands().length > 0) {
        event.preventDefault();
        selectHighlightedCommand();
        return;
      }
    }

    if (event.key === "Tab" && event.shiftKey) {
      if (cycleThinkingShortcut(event)) {
        event.preventDefault();
        cycleThinking().catch(() => {});
      }
      return;
    }
    if (event.key === "Tab" && toggleConversationPromptFocus(event, conversationScroll)) return;

    if (event.key === "Enter" && !event.shiftKey && automaticComposerFocusEnabled()) {
      event.preventDefault();
      promptForm.requestSubmit();
    }
  });

  promptTextarea?.addEventListener("paste", (event) => {
    const items = [...(event.clipboardData?.items || [])];
    const files = items.map((item) => item.kind === "file" ? item.getAsFile() : null).filter(Boolean);
    if (addImageFiles(files)) event.preventDefault();
  });

  promptForm?.addEventListener("dragover", (event) => {
    if ([...(event.dataTransfer?.items || [])].some((item) => item.type.startsWith("image/"))) {
      event.preventDefault();
    }
  });

  promptForm?.addEventListener("drop", (event) => {
    const files = event.dataTransfer?.files || [];
    const hasImage = [...files].some((file) => file.type.startsWith("image/"));
    if (!hasImage) return;

    event.preventDefault();
    addImageFiles(files);
  });

  imageInput?.addEventListener("change", () => {
    addImageFiles(imageInput.files || []);
    imageInput.value = "";
  });

  reconnectButton?.addEventListener("click", reconnectSession);
  promptTextarea?.addEventListener("input", () => {
    resizePromptTextarea();
    persistStoredComposerDraft();
    if (!commandList) return;

    updateCommandListForPrompt();
  });

  promptForm?.addEventListener("submit", submitPrompt);
  abortForm?.addEventListener("submit", submitAbort);
}

function copyTargetText(button) {
  if (button.dataset.copyTarget === "code-block") {
    const block = button.closest(".message-code-block")?.querySelector("pre");
    return block?.innerText || block?.textContent;
  }

  const body = button.closest(".message")?.querySelector(".message-body");
  if (!body) return "";
  if (body.dataset.plainText) return body.dataset.plainText;

  const clone = body.cloneNode(true);
  clone.querySelectorAll?.(".code-block-copy-button").forEach((copyButton) => copyButton.remove());
  return clone.innerText || clone.textContent;
}

async function copyText(text) {
  if (window.gripiElectron?.copyText) {
    const result = await window.gripiElectron.copyText(text);
    if (result?.ok) return true;
  }

  if (navigator.clipboard?.writeText && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch (_error) {}
  }

  const textarea = document.createElement("textarea");
  textarea.value = text;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.left = "-9999px";
  textarea.style.top = "0";
  document.body.append(textarea);
  textarea.select();

  try {
    return document.execCommand("copy");
  } finally {
    textarea.remove();
  }
}

function resetSessionViewState() {
  currentSessionFindController.close({ restoreFocus: false });
  conversationController.reset();
  sessionViewGeneration += 1;
  clearTimeout(eventPollTimer);
  sidebarController.pause();
  clearTimeout(eventPollResumeTimer);
  abortEventPoll();
  eventPollTimer = null;
  eventPollResumeTimer = null;
  liveMessageRenderer.resetLiveAssistantTracking();
  liveMessageRenderer.resetLiveCompactionTracking();
  liveAgentRunning = false;
  liveBusySince = null;
  liveErrorSeen = false;
  resetEventPollBackoff();
  stopWaitingForOutput();
  lastEventSeq = 0;
  hideReconnectBanner();
  clearAttachments();
  const modelModal = document.querySelector('[data-modal="model-settings-modal"]');
  if (modelModal) modelModal.hidden = true;
  modelSettingsOperationGeneration += 1;
  document.body.classList.toggle("modal-open", modalIsOpen());
}

function replaceNewSessionModalHtml(html) {
  const currentModal = document.querySelector('[data-modal="new-session-modal"]');
  if (!html || !currentModal) return;

  newSessionFormController.destroy(currentModal);
  projectSelectController.destroy(currentModal);
  currentModal.outerHTML = html;
  const replacementModal = document.querySelector('[data-modal="new-session-modal"]');
  projectSelectController.initialize(replacementModal);
  newSessionFormController.initialize(replacementModal);
}

function replaceForkSessionModalHtml(html) {
  const currentModal = document.querySelector('[data-modal="fork-session-modal"]');
  if (!html || !currentModal) return;

  currentModal.outerHTML = html;
}

function showSessionSwitching() {
  document.body.classList.add("session-switching");
}

function hideSessionSwitching() {
  document.body.classList.remove("session-switching");
}

function conversationScrollSnapshot() {
  if (!conversationScroll) return null;

  const scrollRect = conversationScroll.getBoundingClientRect();
  const anchor = [...conversationScroll.querySelectorAll("[data-message-fingerprint]")]
    .find((message) => message.getBoundingClientRect().bottom > scrollRect.top);
  return {
    top: conversationScroll.scrollTop,
    nearBottom: conversationScroll.scrollHeight - conversationScroll.scrollTop - conversationScroll.clientHeight < 80,
    anchorFingerprint: anchor?.dataset.messageFingerprint || null,
    anchorOffset: anchor ? anchor.getBoundingClientRect().top - scrollRect.top : null
  };
}

async function switchSession(url, { push = true, focus = true, preserveScroll = false } = {}) {
  const scrollSnapshot = preserveScroll ? conversationScrollSnapshot() : null;
  persistStoredComposerDraft();
  sidebarController.invalidate({ clearSessionsLimit: true });
  const switchGeneration = ++sessionSwitchGeneration;
  const refreshRequestVersion = sidebarController.refreshRequestVersion;
  let navigatingAway = false;
  showSessionSwitching();
  resetSessionViewState();
  try {
    const response = await fetch(sessionFragmentUrl(url), { headers: { "Accept": "application/json" } });
    if (switchGeneration !== sessionSwitchGeneration) return false;
    if (!response.ok) {
      navigatingAway = true;
      window.location.href = url;
      return false;
    }

    const payload = await response.json();
    if (switchGeneration !== sessionSwitchGeneration) return false;
    resetSessionViewState();
    sidebarController.replace(payload.sidebar_html, { notify: false });
    conversationPanel.outerHTML = payload.conversation_html;
    replaceNewSessionModalHtml(payload.new_session_modal_html);
    replaceForkSessionModalHtml(payload.fork_session_modal_html);
    bindSessionDom();
    bindSessionControls();
    if (push) history.pushState({ session: payload.session }, payload.title || "", payload.url || url);
    document.title = payload.title ? `${payload.title} · GRIPi` : "GRIPi";
    sidebarController.closeMobile();
    initializeSessionView({ focus, scrollSnapshot });
    if (refreshRequestVersion !== sidebarController.refreshRequestVersion) sidebarController.scheduleRefresh(0);
    return true;
  } catch (_error) {
    if (switchGeneration !== sessionSwitchGeneration) return false;
    navigatingAway = true;
    window.location.href = url;
    return false;
  } finally {
    if (!navigatingAway && switchGeneration === sessionSwitchGeneration) hideSessionSwitching();
  }
}

function enterSessionShortcutMode() {
  document.body.classList.add("session-shortcuts-visible");
}

function exitSessionShortcutMode() {
  const wasVisible = sessionShortcutsVisible();
  document.body.classList.remove("session-shortcuts-visible");
  if (wasVisible) sidebarController.scheduleRefresh(0);
}

function currentSessionPath() {
  return promptSessionInput?.value || new URLSearchParams(window.location.search).get("session") || "";
}

async function markCurrentSessionRead() {
  const sessionPath = currentSessionPath();
  if (!sessionPath || sidebarController.element) return;
  if (document.hidden || !document.hasFocus()) {
    markReadAfterVisible = true;
    return;
  }
  markReadAfterVisible = false;
  if (markReadInFlight) {
    markReadQueued = true;
    return;
  }

  markReadInFlight = true;
  const body = new URLSearchParams({ session: sessionPath });
  try {
    await fetch("/sessions/mark_read", { method: "POST", body });
  } catch (_error) {
  } finally {
    markReadInFlight = false;
    if (markReadQueued) {
      markReadQueued = false;
      markCurrentSessionRead();
    }
  }
}

async function openRecentSessionShortcut(shortcut) {
  const link = sidebarController.element?.querySelector(`.recent-session[data-session-shortcut="${shortcut}"]`);
  if (!link) return false;
  if (currentSessionPath() === link.dataset.sessionPath) return true;
  const switched = await switchSession(link.href, { push: true, focus: true });
  if (switched && currentSessionPath() !== link.dataset.sessionPath) window.location.href = link.href;
  return switched;
}

function sessionShortcutsVisible() {
  return document.body.classList.contains("session-shortcuts-visible");
}

function openNewSessionModal() {
  const modal = document.querySelector('[data-modal="new-session-modal"]');
  newSessionFormController.open(modal?.querySelector(".new-session-cwd-form"));
  openModal(modal);
}

function modalIsOpen() {
  return !!document.querySelector("[data-modal]:not([hidden])");
}

function openModal(modal) {
  if (!modal) return;
  modal.hidden = false;
  clearTimeout(eventPollTimer);
  sidebarController.pause();
  browserAccessController.pause();
  workspaceAccessController.pause();
  abortEventPoll();
  document.body.classList.add("modal-open");
  const defaultFocus = modal.querySelector("[data-modal-default-focus]:not(:disabled)");
  (defaultFocus || modal.querySelector("input, select, textarea, button"))?.focus();
}

function closeModal(modal) {
  if (!modal) return;
  if (modal.dataset.modal === "new-session-modal") newSessionFormController.close(modal.querySelector(".new-session-cwd-form"));
  modal.hidden = true;
  if (modal.dataset.modal === "model-settings-modal") modelSettingsOperationGeneration += 1;
  document.body.classList.toggle("modal-open", modalIsOpen());
  if (!modalIsOpen() && !document.hidden) {
    scheduleNextEventPoll(0);
    sidebarController.scheduleRefresh(0);
    browserAccessController.resume();
    workspaceAccessController.resume();
    focusPromptAfterModalClose(modal);
  }
}

function focusPromptAfterModalClose(modal) {
  if (modal?.dataset.modal === "model-settings-modal") {
    const modelButton = sessionStatusBar?.querySelector('[data-status-key="model"]:not(:disabled)');
    (modelButton || conversationScroll)?.focus({ preventScroll: true });
  } else if (modal?.dataset.modal === "new-session-modal") {
    syncComposerFocus();
  }
}

function normalLeftClick(event) {
  return event.button === 0 && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey;
}

function setForkSessionStatus(modal, text) {
  const list = modal?.querySelector("[data-fork-session-list]");
  if (!list) return;
  list.replaceChildren();
  delete list.dataset.loaded;
  const status = document.createElement("p");
  status.className = "fork-session-status";
  status.dataset.forkSessionStatus = "";
  status.textContent = text;
  list.append(status);
}

async function loadForkMessages(modal) {
  const list = modal?.querySelector("[data-fork-session-list]");
  const url = list?.dataset.forkMessagesUrl;
  if (!list || !url || list.dataset.loaded === "true" || list.dataset.loading === "true") return;

  list.dataset.loading = "true";
  setForkSessionStatus(modal, "Loading fork points…");
  try {
    const response = await fetch(url, { headers: { "Accept": "application/json" } });
    const payload = await response.json().catch(() => null);
    if (!response.ok || !payload) throw new Error("fork messages failed");
    const messages = Array.isArray(payload.messages) ? payload.messages : [];
    list.replaceChildren();
    if (messages.length === 0) {
      setForkSessionStatus(modal, "No previous user messages are available to fork.");
      return;
    }
    messages.forEach((message) => {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "fork-session-option";
      button.dataset.forkEntryId = message.entryId || message.entry_id || "";
      button.textContent = message.text || "Untitled prompt";
      list.append(button);
    });
    list.dataset.loaded = "true";
  } catch (_error) {
    setForkSessionStatus(modal, "Could not load fork points.");
  } finally {
    delete list.dataset.loading;
  }
}

function setTreeSessionStatus(modal, text) {
  const list = modal?.querySelector("[data-tree-session-list]");
  if (!list) return;
  list.replaceChildren();
  delete list.dataset.loaded;
  const status = document.createElement("p");
  status.className = "fork-session-status";
  status.dataset.treeSessionStatus = "";
  status.textContent = text;
  list.append(status);
}

async function loadTreeEntries(modal) {
  const list = modal?.querySelector("[data-tree-session-list]");
  const url = list?.dataset.treeEntriesUrl;
  if (!list || !url || list.dataset.loaded === "true" || list.dataset.loading === "true") return;

  list.dataset.loading = "true";
  setTreeSessionStatus(modal, "Loading session tree…");
  try {
    const response = await fetch(url, { headers: { "Accept": "application/json" } });
    const payload = await response.json().catch(() => null);
    if (!response.ok || !payload) throw new Error("tree entries failed");
    const entries = Array.isArray(payload.entries) ? payload.entries : [];
    list.replaceChildren();
    if (entries.length === 0) {
      setTreeSessionStatus(modal, "No session tree entries are available.");
      return;
    }
    entries.forEach((entry) => {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "fork-session-option";
      button.classList.toggle("is-current-tree-entry", !!entry.current);
      button.dataset.treeEntryId = entry.entryId || entry.entry_id || "";
      if (entry.editorText !== undefined || entry.editor_text !== undefined) button.dataset.treeEditorText = entry.editorText ?? entry.editor_text ?? "";

      const role = document.createElement("span");
      role.className = "tree-session-role";
      role.textContent = entry.role || entry.type || "entry";
      const text = document.createElement("span");
      text.textContent = ` ${entry.text || "Untitled entry"}`;
      button.append(role, text);
      if (entry.current) button.append(treeSessionBadge("Current", "current"));
      if (entry.latest) button.append(treeSessionBadge("Latest", "latest"));
      list.append(button);
    });
    list.dataset.loaded = "true";
  } catch (_error) {
    setTreeSessionStatus(modal, "Could not load session tree.");
  } finally {
    delete list.dataset.loading;
  }
}

function treeSessionBadge(label, kind) {
  const badge = document.createElement("span");
  badge.className = `tree-session-badge tree-session-badge--${kind}`;
  badge.textContent = label;
  return badge;
}

function addSessionViewFormParams(formData) {
  const project = new URLSearchParams(window.location.search).get("project");
  if (project) formData.set("project", project);
  const sessionSearch = sidebarController.activeSearch();
  if (sessionSearch) formData.set("session_search", sessionSearch);
  if (new URLSearchParams(window.location.search).get("session_only") === "1") formData.set("session_only", "1");
}

async function switchToBranchedSession(payload, { promptText = null } = {}) {
  const switched = await switchSession(payload.redirect || `/?session=${encodeURIComponent(payload.session)}`, { push: true, focus: true });
  if (switched && promptText !== null && promptTextarea) {
    promptTextarea.value = promptText;
    resizePromptTextarea();
    syncComposerFocus();
  }
  return switched;
}

function openForkSessionModal() {
  const modal = document.querySelector('[data-modal="fork-session-modal"]');
  openModal(modal);
  loadForkMessages(modal).catch(() => {});
}

function openTreeSessionModal() {
  const modal = document.querySelector('[data-modal="tree-session-modal"]');
  openModal(modal);
  loadTreeEntries(modal).catch(() => {});
}

document.addEventListener("click", (event) => {
  const opener = event.target.closest("[data-modal-open]");
  if (opener) {
    event.preventDefault();
    const modal = document.querySelector(`[data-modal="${opener.dataset.modalOpen}"]`);
    if (opener.dataset.modalOpen === "new-session-modal") {
      openNewSessionModal();
      return;
    }
    if (opener.dataset.modalOpen === "fork-session-modal") {
      openForkSessionModal();
      return;
    }
    if (opener.dataset.modalOpen === "tree-session-modal") {
      openTreeSessionModal();
      return;
    }
    if (opener.dataset.modalOpen === "model-settings-modal") {
      openModelSettingsModal();
      return;
    }
    openModal(modal);
    return;
  }

  const forkOption = event.target.closest("[data-fork-entry-id]");
  if (forkOption) {
    event.preventDefault();
    const modal = forkOption.closest("[data-modal]");
    const originalForkText = forkOption.textContent;
    const formData = new FormData();
    formData.set("session", currentSessionPath());
    formData.set("entry_id", forkOption.dataset.forkEntryId);
    addSessionViewFormParams(formData);
    forkOption.disabled = true;
    forkOption.textContent = "Forking…";
    showSessionSwitching();
    fetch("/sessions/fork", { method: "POST", body: formData, headers: { "Accept": "application/json" } })
      .then(async (response) => {
        const payload = await response.json().catch(() => null);
        if (!response.ok || !payload || payload.cancelled) throw new Error("fork failed");
        closeModal(modal);
        await switchToBranchedSession(payload, { promptText: payload.text || "" });
      })
      .catch(() => {
        forkOption.disabled = false;
        forkOption.textContent = originalForkText;
        if (modal) {
          setForkSessionStatus(modal, "Could not fork this session.");
        } else {
          showStatus("Could not fork this session", true);
        }
      })
      .finally(() => {
        hideSessionSwitching();
      });
    return;
  }

  const takeoverButton = event.target.closest("[data-session-takeover]");
  if (takeoverButton) {
    event.preventDefault();
    const originalText = takeoverButton.textContent;
    const banner = takeoverButton.closest("[data-session-sync-banner]");
    const errorOutput = banner?.querySelector("[data-session-sync-error]");
    const formData = new FormData();
    formData.set("session", currentSessionPath());
    takeoverButton.disabled = true;
    takeoverButton.textContent = "Taking over…";
    if (errorOutput) errorOutput.hidden = true;
    fetch("/sessions/takeover", { method: "POST", body: formData, headers: { "Accept": "application/json" } })
      .then(async (response) => {
        const payload = await response.json().catch(() => null);
        if (!response.ok || !payload?.ok) throw new Error(payload?.error || "Could not take over session");
        await refreshCurrentSessionPreservingComposer();
        scheduleNextEventPoll(0);
      })
      .catch((error) => {
        takeoverButton.disabled = false;
        takeoverButton.textContent = originalText;
        if (errorOutput) {
          errorOutput.textContent = error.message;
          errorOutput.hidden = false;
        }
      });
    return;
  }

  const treeOption = event.target.closest("[data-tree-entry-id]");
  if (treeOption) {
    event.preventDefault();
    const modal = treeOption.closest("[data-modal]");
    const originalTreeText = treeOption.textContent;
    const formData = new FormData();
    formData.set("session", currentSessionPath());
    formData.set("entry_id", treeOption.dataset.treeEntryId);
    addSessionViewFormParams(formData);
    treeOption.disabled = true;
    treeOption.textContent = "Switching…";
    fetch("/sessions/tree", { method: "POST", body: formData, headers: { "Accept": "application/json" } })
      .then(async (response) => {
        const payload = await response.json().catch(() => null);
        if (!response.ok || !payload) throw new Error("tree failed");
        if (payload.cancelled) {
          treeOption.disabled = false;
          treeOption.textContent = originalTreeText;
          if (modal) setTreeSessionStatus(modal, "Tree navigation cancelled.");
          return;
        }
        closeModal(modal);
        if (treeOption.dataset.treeEditorText !== undefined && promptTextarea) {
          promptTextarea.value = treeOption.dataset.treeEditorText;
          resizePromptTextarea();
        }
        await refreshCurrentSessionPreservingComposer();
        setComposerState("idle", "", { focus: false });
        syncComposerFocus();
        showStatus("Tree position selected", true);
        scheduleNextEventPoll(0);
      })
      .catch(() => {
        treeOption.disabled = false;
        treeOption.textContent = originalTreeText;
        if (modal) {
          setTreeSessionStatus(modal, "Could not switch to this tree entry.");
        } else {
          showStatus("Could not switch session tree", true);
        }
      });
    return;
  }

  const closer = event.target.closest("[data-modal-close]");
  if (closer) {
    event.preventDefault();
    closeModal(closer.closest("[data-modal]"));
    return;
  }

  const modal = event.target.matches("[data-modal]") ? event.target : null;
  if (modal) closeModal(modal);
});

document.addEventListener("input", (event) => {
  if (event.target.closest("[data-model-search]")) renderModelSettingsModels();
});

document.addEventListener("submit", (event) => {
  const form = event.target.closest("[data-model-settings-form]");
  if (!form) return;
  event.preventDefault();
  applyModelSettings(form).catch(() => {});
});

document.addEventListener("click", (event) => {
  const toggle = event.target.closest("[data-notification-toggle]");
  if (!toggle) return;

  event.preventDefault();
  toggleNotifications().catch(() => {
    window.location.href = "/notification-test";
  });
});

document.addEventListener("submit", async (event) => {
  const cloneForm = event.target.closest(".clone-session-form");
  if (!cloneForm) return;

  event.preventDefault();
  if (cloneForm.dataset.submitting === "true") return;
  const submit = cloneForm.querySelector('button[type="submit"]');
  const originalSubmitText = submit?.textContent || "Clone";
  const formData = new FormData(cloneForm);
  addSessionViewFormParams(formData);
  cloneForm.dataset.submitting = "true";
  if (submit) {
    submit.disabled = true;
    submit.textContent = "Cloning…";
  }
  showSessionSwitching();
  try {
    const response = await fetch(cloneForm.action, { method: "POST", body: formData, headers: { "Accept": "application/json" } });
    const payload = await response.json().catch(() => null);
    if (!response.ok || !payload || payload.cancelled) throw new Error("clone failed");
    await switchToBranchedSession(payload);
  } catch (_error) {
    showStatus("Could not clone session", true);
    if (submit) {
      submit.disabled = false;
      submit.textContent = originalSubmitText;
    }
  } finally {
    delete cloneForm.dataset.submitting;
    hideSessionSwitching();
  }
});

document.addEventListener("submit", async (event) => {
  const form = event.target.closest(".new-session-cwd-form");
  if (!form) return;

  event.preventDefault();
  const submit = form.querySelector("[data-new-session-submit]");
  if (submit?.disabled || form.dataset.submitting === "true") return;

  newSessionFormController.sync(form);
  const formData = new FormData(form);
  addSessionViewFormParams(formData);
  const modal = form.closest("[data-modal]");
  const originalSubmitText = submit?.textContent || "Start session";
  form.dataset.submitting = "true";
  if (submit) {
    submit.disabled = true;
    submit.textContent = "Starting…";
  }
  showSessionSwitching();
  try {
    const response = await fetch(form.action, { method: "POST", body: formData, headers: { "Accept": "application/json" } });
    if (!response.ok) {
      const payload = await response.json().catch(() => null);
      newSessionFormController.setValidationState(form, "invalid", payload?.error || "Path must be an existing directory.");
      if (submit) submit.textContent = originalSubmitText;
      return;
    }
    const payload = await response.json();
    closeModal(modal);
    await switchSession(payload.redirect || `/?session=${encodeURIComponent(payload.session)}`, { push: true, focus: true });
  } catch (_error) {
    newSessionFormController.setValidationState(form, "invalid", "Could not start the session. Try again.");
    if (submit) {
      submit.disabled = false;
      submit.textContent = originalSubmitText;
    }
  } finally {
    delete form.dataset.submitting;
    hideSessionSwitching();
  }
});

function focusPromptAfterDesktopServerActivation() {
  syncComposerFocus();
}

window.addEventListener("gripi:new-session-requested", () => openNewSessionModal());
window.addEventListener("gripi:current-session-find-requested", requestCurrentSessionFind);
window.addEventListener("gripi:current-session-find-navigation-requested", (event) => requestCurrentSessionFindNavigation(event.detail));
window.addEventListener("gripi:session-search-requested", requestSessionSearch);
window.addEventListener("gripi:desktop-server-activated", focusPromptAfterDesktopServerActivation);

function handleModelSettingsModalTab(event) {
  if (event.key !== "Tab") return;
  const modal = document.querySelector('[data-modal="model-settings-modal"]:not([hidden])');
  if (!modal) return;
  const focusable = [...modal.querySelectorAll('button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])')]
    .filter((element) => !element.closest("[hidden]"));
  if (focusable.length === 0) return;
  const first = focusable[0];
  const last = focusable[focusable.length - 1];
  if (!focusable.includes(document.activeElement)) {
    event.preventDefault();
    first.focus();
  } else if ((!event.shiftKey && document.activeElement === last) || (event.shiftKey && document.activeElement === first)) {
    event.preventDefault();
    (event.shiftKey ? last : first).focus();
  }
}

document.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && modalIsOpen() && !event.defaultPrevented) {
    event.preventDefault();
    const openModalElement = document.querySelector("[data-modal]:not([hidden])");
    const cwdForm = openModalElement?.querySelector(".new-session-cwd-form");
    if (newSessionFormController.closeSuggestions(cwdForm)) return;
    closeModal(openModalElement);
    return;
  }

  handleModelSettingsModalTab(event);
  if (modalIsOpen()) return;

  if (handleSessionSearchShortcut(event)) return;
  if (sidebarController.closeSearch(event)) {
    syncComposerFocus();
    return;
  }
  if (handleCurrentSessionFindShortcut(event)) return;
  if (handleCurrentSessionFindNavigationShortcut(event)) return;
  if (event.key === "Escape" && currentSessionFindController.open) {
    event.preventDefault();
    currentSessionFindController.close();
    return;
  }

  if (isCtrlOrMetaShortcut(event, "n") && !event.shiftKey) {
    event.preventDefault();
    openNewSessionModal();
    return;
  }

  if (event.key === "Escape" && confirmOrStopRunningTask(event)) return;

  if (event.key === "Control") {
    enterSessionShortcutMode();
    return;
  }

  if (!sessionShortcutsVisible()) return;
  if (event.key === "Escape") {
    event.preventDefault();
    exitSessionShortcutMode();
    return;
  }

  if (event.altKey || !event.ctrlKey) return;
  const shortcut = recentSessionShortcutFromEvent(event);
  if (shortcut) {
    event.preventDefault();
    if (event.repeat) return;
    openRecentSessionShortcut(shortcut).catch(() => {});
  }
});

document.addEventListener("keyup", (event) => {
  if (event.key === "Control") exitSessionShortcutMode();
});

window.addEventListener("blur", exitSessionShortcutMode);

document.addEventListener("click", (event) => {
  if (!event.target.closest(".session-sidebar")) exitSessionShortcutMode();
});

document.addEventListener("gripi:sidebar-project-filtered", (event) => {
  replaceNewSessionModalHtml(event.detail.modalHtml);
});

document.addEventListener("gripi:sidebar-selected-title", (event) => {
  updateSessionHeaderName(event.detail.title);
});

document.addEventListener("click", async (event) => {
  const link = event.target.closest(".session-sidebar a.session");
  exitSessionShortcutMode();
  if (!link || !normalLeftClick(event)) return;

  event.preventDefault();
  if (link.classList.contains("selected")) {
    sidebarController.closeMobile();
    return;
  }

  await switchSession(link.href, { push: true, focus: true });
});

document.addEventListener("submit", async (event) => {
  const form = event.target.closest('form[action="/sessions/new"]');
  if (!form) return;

  event.preventDefault();
  const switchGeneration = sessionSwitchGeneration;
  const viewGeneration = sessionViewGeneration;
  let navigatingAway = false;
  showSessionSwitching();
  try {
    const formData = new FormData(form);
    addSessionViewFormParams(formData);
    const response = await fetch(form.action, { method: "POST", body: formData, headers: { "Accept": "application/json" } });
    if (switchGeneration !== sessionSwitchGeneration || viewGeneration !== sessionViewGeneration) return;
    if (!response.ok) {
      navigatingAway = true;
      form.submit();
      return;
    }

    const payload = await response.json();
    if (switchGeneration !== sessionSwitchGeneration || viewGeneration !== sessionViewGeneration) return;
    await switchSession(payload.redirect || `/?session=${encodeURIComponent(payload.session)}`, { push: true, focus: true });
  } finally {
    if (!navigatingAway && switchGeneration === sessionSwitchGeneration && viewGeneration === sessionViewGeneration) hideSessionSwitching();
  }
});

document.addEventListener("click", (event) => {
  const button = event.target.closest("[data-tool-output-toggle]");
  if (!button) return;

  const collapse = button.closest("[data-tool-output-collapse]");
  const body = collapse?.querySelector("[data-tool-output-body]");
  const fullTemplate = collapse?.querySelector("[data-tool-output-full]");
  const control = collapse?.querySelector("[data-tool-output-collapse-control]");
  if (!collapse || !body || !fullTemplate || !control) return;

  collapse.dataset.expanded = "true";
  collapse.dataset.collapsed = "false";
  button.setAttribute("aria-expanded", "true");
  control.hidden = true;
  body.replaceChildren(...Array.from(fullTemplate.content.cloneNode(true).childNodes));
  activateToolOutputRegion(body, { focus: true });
  body.scrollTop = body.scrollHeight;
});

document.addEventListener("click", async (event) => {
  const button = event.target.closest("[data-copy-target]");
  if (!button) return;

  const text = copyTargetText(button);
  if (!text) return;

  const original = button.textContent;
  try {
    const copied = await copyText(text);
    button.textContent = copied ? "Copied" : "Copy failed";
  } catch (_error) {
    button.textContent = "Copy failed";
  }
  setTimeout(() => { button.textContent = original; }, 1200);
});

document.addEventListener("click", (event) => {
  const command = event.target.closest(".command");
  if (!command || !commandList?.contains(command) || !promptTextarea) return;

  selectCommand(command);
});

function restorePreservedConversationScroll(scrollSnapshot) {
  if (!scrollSnapshot || scrollSnapshot.nearBottom || !conversationScroll) return false;

  const anchor = scrollSnapshot.anchorFingerprint && [...conversationScroll.querySelectorAll("[data-message-fingerprint]")]
    .find((message) => message.dataset.messageFingerprint === scrollSnapshot.anchorFingerprint);
  if (anchor) {
    const scrollTop = conversationScroll.getBoundingClientRect().top;
    conversationScroll.scrollTop += anchor.getBoundingClientRect().top - scrollTop - scrollSnapshot.anchorOffset;
  } else {
    conversationScroll.scrollTop = Math.min(scrollSnapshot.top, Math.max(0, conversationScroll.scrollHeight - conversationScroll.clientHeight));
  }
  conversationController.stopAutoFollow();
  return true;
}

function initializeSessionView({ focus = true, scrollSnapshot = null } = {}) {
  const generation = sessionViewGeneration;
  projectSelectController.initialize(document.querySelector('[data-modal="new-session-modal"]'));
  newSessionFormController.initialize();
  ensureNotificationWorker().catch(() => {});
  browserAccessController.resume();
  workspaceAccessController.resume();
  if (liveOutput) {
    enhanceMarkdownCodeBlocks(conversationScroll);
    resetEventCursor();
    const initialComposerState = liveOutput.dataset.composerState;
    const initialComposerStateSince = Number(liveOutput.dataset.composerStateSince || 0);
    const initialComposerCompacting = liveOutput.dataset.composerCompacting === "true";
    liveBusySince = Number(liveOutput.dataset.composerBusySince || 0) || null;
    const initialComposerLabel = initialComposerCompacting ? "Compacting…" : "Pi is running…";
    liveAgentRunning = liveOutput.dataset.agentRunning === "true";
    if (initialComposerState === "running") setComposerState(initialComposerState, initialComposerLabel, { since: initialComposerStateSince, focus: false });
    if (initialComposerCompacting) liveMessageRenderer.appendPendingCompactionMessage(new Date(initialComposerStateSince || Date.now()));
    liveMessageRenderer.restoreActiveToolExecutions();
    scheduleNextEventPoll(0);
    if (!scrollSnapshot || scrollSnapshot.nearBottom) conversationController.positionInitialAtBottom();
    requestAnimationFrame(() => {
      if (generation !== sessionViewGeneration) return;
      loadStoredComposerDraft();
      updatePromptPlaceholder();
      resizePromptTextarea();
      if (focus) syncComposerFocus();
      if (!restorePreservedConversationScroll(scrollSnapshot)) conversationController.forceInitialBottomFollow();
    });
  }
  sidebarController.scheduleRefresh();
  gatewayUpdateController.check().catch(() => {});
}

window.addEventListener("resize", updatePromptPlaceholder);
window.addEventListener("visibilitychange", () => {
  if (document.hidden) {
    hiddenAt = Date.now();
    return;
  }

  const hiddenDuration = hiddenAt ? Date.now() - hiddenAt : 0;
  hiddenAt = null;
  if (hiddenDuration > 5000) {
    resumeEventPolling(hiddenDuration).catch(() => {});
  } else {
    scheduleNextEventPoll(0);
  }
  if (markReadAfterVisible) markCurrentSessionRead();
  sidebarController.scheduleRefresh();
});
window.addEventListener("pageshow", () => resumeEventPolling().catch(() => {}));
window.addEventListener("focus", () => {
  if (markReadAfterVisible) markCurrentSessionRead();
  resumeEventPolling().catch(() => {});
});
window.addEventListener("online", () => resumeEventPolling().catch(() => {}));
window.addEventListener("popstate", () => switchSession(window.location.href, { push: false, focus: true }));

function bootstrapPage() {
  gatewayUpdateController.cleanNavigation();
  sidebarController.initialize();
  bindPageLifetimeControls();
  bindSessionDom();
  bindSessionControls();
  initializeSessionView();
  gatewayUpdateController.resume();
}

bootstrapPage();
