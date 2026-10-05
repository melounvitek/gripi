import { ESCAPE_STOP_CONFIRMATION_WINDOW_MS, SESSION_SWITCH_TIMEOUT_MS, STALE_SESSION_REFRESH_AFTER_MS } from "./constants.js";
import { AsyncGeneration } from "./async_generation.js";
import { parseNativeBash } from "./bash.js";
import { downloadResponse } from "./downloads.js";
import {
  eventErrorText,
  eventStatusText,
  eventTimestamp,
  extensionUiRequestNotice,
  formatWaitDuration,
  imageAttachmentLabel,
  notificationReplyPreview,
  sessionAuthGuidanceSlashCommand,
  sessionCloneSlashCommand,
  sessionCompactSlashCommand,
  sessionExportSlashCommand,
  sessionForkSlashCommand,
  sessionModelSlashCommand,
  sessionNameFromEvent,
  sessionNameSlashCommand,
  sessionNewSlashCommand,
  sessionReloadSlashCommand,
  sessionTreeSlashCommand,
  stableTextHash
} from "./formatting.js";
import { matchingPickerModels, modelSettingsKey, scopedPickerModels, selectedThinkingLevel, sortedPickerModels, supportedThinkingLevels } from "./model.js";
import {
  currentSessionFindNavigationShortcut,
  isCtrlOrMetaShortcut,
  keyboardScrollKey,
  recentSessionShortcutFromEvent,
  sessionSearchShortcut
} from "./shortcuts.js";
import { sessionFragmentUrl, sessionUrl } from "./urls.js";
import { GatewayUpdateController } from "./gateway_update_controller.js";
import { ResourceUsageController } from "./resource_usage_controller.js";
import { BrowserAccessRequestController, WorkspaceAccessRequestController } from "./access_request_controllers.js";
import { ProjectSelectController } from "./project_select_controller.js";
import { NewSessionFormController } from "./new_session_form_controller.js";
import { SessionActionsController } from "./session_actions_controller.js";
import { SessionTagsController } from "./session_tags_controller.js";
import { SidebarController } from "./sidebar_controller.js";
import { ConversationController } from "./conversation_controller.js";
import { CommandPaletteController } from "./command_palette_controller.js";
import { EnvironmentController } from "./environment_controller.js";
import { ComposerAutocompleteController } from "./composer_autocomplete_controller.js";
import { CurrentSessionFindController } from "./current_session_find_controller.js";
import { LiveMessageParser } from "./live_message_parser.js";
import { LiveMessageRenderer } from "./live_message_renderer.js";
import { ServerMarkdownRenderer } from "./server_markdown_renderer.js";
import { activateToolOutputRegion, enhanceMarkdownCodeBlocks, enhanceMessageLinks, movePickerCursor } from "./dom.js";
import { eventPollCurrent, eventPollingDelay } from "./polling.js";
import { extensionUiRequestExpired, extensionUiResponseDisposition } from "./extension_ui.js";
import { TreeSessionController } from "./tree_session_controller.js";
import { ImageViewerController } from "./image_viewer_controller.js";
import { NotificationPresenceController } from "./notification_presence.js";
import { WebPushController } from "./web_push.js";

const gatewayUpdateController = new GatewayUpdateController(document, window);
const resourceUsageController = new ResourceUsageController(document, window);
const notificationPresenceController = new NotificationPresenceController(document, window, currentSessionPath);
const webPushController = new WebPushController(window, navigator);
const notifyAccessRequest = (title, body, tag) =>
  showGripiNotification(title, body, window.location.href, tag).catch(() => {});
const browserAccessController = new BrowserAccessRequestController(document, notifyAccessRequest);
const workspaceAccessController = new WorkspaceAccessRequestController(document, notifyAccessRequest);
const projectSelectController = new ProjectSelectController(document, window);
const newSessionFormController = new NewSessionFormController(document, window);
const sidebarController = new SidebarController(
  document,
  window,
  projectSelectController,
  gatewayUpdateController,
  (name, body, url, tag) => {
    if (webPushController.enabled()) return;
    showGripiNotification(name, body, url, tag).catch(() => {});
  }
);
const sessionTagsController = new SessionTagsController(document, window, {
  openModal,
  closeModal,
  invalidate: () => sidebarController.invalidate(),
  refresh: () => sidebarController.refresh({ force: true }),
  filter: (url) => sidebarController.applyFilters(url)
});
const sessionActionsController = new SessionActionsController(document, window, {
  editTags: (target) => sessionTagsController.open(target.path, target.row.querySelector("[data-session-actions-toggle]")),
  currentSessionPath: () => currentSessionPath(),
  detachSession: (paths) => detachSession(paths).catch(() => {}),
  openModal: (modal) => openModal(modal),
  closeModal: (modal) => closeModal(modal),
  refresh: () => sidebarController.refresh({ force: true }),
  renamed: ({ session, name }) => sidebarController.updateSessionName(session, name),
  showStatus: (message) => showStatus(message, true)
});

let conversationPanel = null;
let liveOutput = null;
let promptForm = null;
let abortForm = null;
let promptTextarea = null;
let promptSessionInput = null;
let sendButton = null;
let sendControl = null;
let sendMenuToggle = null;
let sendMenu = null;
let streamingBehaviorSelection = "steer";
let keyboardStreamingBehaviorOverride = null;
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
let liveAgentRunning = false;
let liveBash = null;
let liveBusySince = null;
let liveErrorText = "";
let liveStatusModel = null;
let liveStatusThinking = null;
let modelSettingsModels = [];
let modelSettingsScopedModels = [];
let modelSettingsVisibleModels = [];
let modelSettingsScope = "all";
let modelSettingsActiveIndex = 0;
let modelSettingsPending = false;
let modelSettingsCurrentModel = null;
let modelSettingsCurrentThinking = "off";
let modelSettingsOperationGeneration = 0;
let thinkingCyclePending = false;
let pendingImages = [];
let escapeStopConfirmationExpiresAt = 0;
let escapeStopConfirmationTimer = null;
const stoppingSessionPaths = new Set();
let eventPollTimer = null;
let eventPollInFlight = false;
let lastEventPollFailed = false;
let eventPollAbortController = null;
let eventPollResumeTimer = null;
let staleSessionRefreshInFlight = false;
let markReadInFlight = false;
const markReadQueued = new Map();
let markReadAfterVisible = null;
let hiddenAt = null;
let lastSessionSyncAt = Date.now();
let lastEventSeq = 0;
let lastQueueSeq = 0;
let queueViewGeneration = 0;
// Match Pi CLI's working indicator: default label and pi-tui Loader frames.
const WORKING_LABEL = "Working";
const COMPOSER_SPINNER_FRAMES = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];
let waitingForOutputSince = null;
let waitingForOutputTimer = null;
let waitingForOutputLabel = WORKING_LABEL;
let composerSpinnerTimer = null;
let composerSpinnerFrame = 0;
let extensionUiModal = null;
let extensionUiForm = null;
let extensionUiTitle = null;
let extensionUiMessage = null;
let extensionUiError = null;
let extensionUiOptions = null;
let extensionUiInput = null;
let extensionUiEditor = null;
let extensionUiSubmit = null;
let extensionUiHint = null;
let extensionWidgetsAboveContainer = null;
let extensionWidgetsBelowContainer = null;
let activeExtensionUiRequest = null;
let extensionUiRequestQueue = [];
let extensionUiTimeoutTimer = null;
let extensionUiDeliveryPending = false;
let extensionUiControlsBound = false;
let extensionWidgets = new Map();
let baseDocumentTitle = document.title;
let extensionDocumentTitle = null;
let lastBoundSessionPath = null;
let emptyEventPollCount = 0;
let sessionViewGeneration = 0;
const sessionSwitchGeneration = new AsyncGeneration();
let sessionSwitchAbortController = null;
let sessionNavigationPending = false;
let promptSubmissionGeneration = 0;
let sessionStatusRequestVersion = 0;
let notificationRegistration = null;
let webPushEnabled = false;
const notifiedFinalReplyKeys = new Set();
let pendingFinalAssistantReply = null;
const MAIN_SESSION_HISTORY_KEY = "gripi-main-session-history";
const conversationController = new ConversationController(document, window);
const currentSessionFindController = new CurrentSessionFindController(document, conversationController);
const composerAutocompleteController = new ComposerAutocompleteController(document, {
  currentSessionPath: () => currentSessionPath()
});
const liveMessageParser = new LiveMessageParser(document.body.dataset.homeDir || "");
const serverMarkdownRenderer = new ServerMarkdownRenderer(document, conversationController);
const imageViewerController = new ImageViewerController(document, window);
const liveMessageRenderer = new LiveMessageRenderer(document, conversationController, liveMessageParser, serverMarkdownRenderer, imageViewerController);
imageViewerController.bind();
conversationController.historyEnhancer = (root) => liveMessageRenderer.hydrateTerminalOutputs(root, { notify: false });
conversationController.historyReconciler = (root) => liveMessageRenderer.reconcilePersistedToolResults(root);
const commandPaletteController = new CommandPaletteController(document, {
  openModal,
  closeModal,
  modalIsOpen,
  currentSessionPath,
  previousSessionPath: () => readMainSessionHistory().previous,
  openSession: (path) => switchSession(sessionUrl(path), { push: true, focus: true }),
  commands: commandPaletteCommands
});
const environmentController = new EnvironmentController(document, window, { openModal });
const treeSessionController = new TreeSessionController(document, window, {
  currentSessionPath: () => currentSessionPath(),
  addSessionViewFormParams: (formData) => addSessionViewFormParams(formData),
  openModal: (modal) => openModal(modal),
  closeModal: (modal) => closeModal(modal),
  showSessionSwitching: () => showSessionSwitching(),
  hideSessionSwitching: () => hideSessionSwitching(),
  restoreEditorText: (text) => {
    if (promptTextarea && !promptTextarea.value) {
      promptTextarea.value = text;
      resizePromptTextarea();
    }
  },
  navigate: async (payload) => {
    if (promptTextarea && !promptTextarea.value && payload?.editorText !== undefined) {
      promptTextarea.value = payload.editorText;
      resizePromptTextarea();
    }
    await refreshCurrentSessionPreservingComposer();
    setComposerState("idle", "", { focus: false });
    syncComposerFocus();
    showStatus("Tree position selected", true);
    scheduleNextEventPoll(0);
  }
});

function bindSessionDom() {
  conversationPanel = document.querySelector(".conversation-panel");
  sidebarController.syncVisibilityToggle();
  liveOutput = document.getElementById("live-output");
  promptForm = document.querySelector(".prompt-form");
  abortForm = document.getElementById("abort-form");
  promptTextarea = promptForm?.querySelector("textarea") || null;
  extensionUiModal = document.querySelector('[data-modal="extension-ui-modal"]');
  extensionUiForm = extensionUiModal?.querySelector("[data-extension-ui-form]") || null;
  extensionUiTitle = extensionUiModal?.querySelector("[data-extension-ui-title]") || null;
  extensionUiMessage = extensionUiModal?.querySelector("[data-extension-ui-message]") || null;
  extensionUiError = extensionUiModal?.querySelector("[data-extension-ui-error]") || null;
  extensionUiOptions = extensionUiModal?.querySelector("[data-extension-ui-options]") || null;
  extensionUiInput = extensionUiModal?.querySelector("[data-extension-ui-input]") || null;
  extensionUiEditor = extensionUiModal?.querySelector("[data-extension-ui-editor]") || null;
  extensionUiSubmit = extensionUiModal?.querySelector("[data-extension-ui-submit]") || null;
  extensionUiHint = extensionUiModal?.querySelector("[data-extension-ui-hint]") || null;
  extensionWidgetsAboveContainer = document.querySelector("[data-extension-widgets-above]");
  extensionWidgetsBelowContainer = document.querySelector("[data-extension-widgets-below]");
  promptSessionInput = promptForm?.querySelector('input[name="session"]') || null;
  sendButton = promptForm?.querySelector(".send-button") || null;
  sendControl = promptForm?.querySelector(".send-control") || null;
  sendMenuToggle = promptForm?.querySelector("[data-send-menu-toggle]") || null;
  sendMenu = promptForm?.querySelector("[data-send-menu]") || null;
  streamingBehaviorSelection = "steer";
  keyboardStreamingBehaviorOverride = null;
  composerStopButton = document.querySelector(".session-header .composer-stop-button") || null;
  attachButton = promptForm?.querySelector(".attach-button") || null;
  attachmentTray = promptForm?.querySelector(".attachment-tray") || null;
  imageInput = promptForm?.querySelector(".image-input") || null;
  composerState = document.querySelector(".composer-state");
  abortButton = document.querySelector(".abort-button");
  commandList = document.getElementById("command-list");
  highlightedCommandIndex = 0;
  projectSelectController.initialize(conversationPanel);
  conversationController.bind(promptTextarea);
  composerAutocompleteController.bind(promptTextarea, document.getElementById("composer-path-list"));
  conversationScroll = conversationController.element;
  liveMessageRenderer.bind();
  currentSessionFindController.bind();
  sessionStatusBar = document.getElementById("session-status-bar");
  const boundSessionPath = currentSessionPath();
  if (boundSessionPath !== lastBoundSessionPath) {
    lastBoundSessionPath = boundSessionPath;
    extensionWidgets.clear();
    baseDocumentTitle = document.title;
    extensionDocumentTitle = null;
  }
  renderExtensionWidgets();
  const existingModelStatus = sessionStatusBar?.querySelector('[data-status-key="model"] .session-status-value')?.textContent || "";
  const existingModelMatch = existingModelStatus.match(/^(.*?)(?:\s+\(([^)]*)\))?$/);
  liveStatusModel = existingModelMatch?.[1] || null;
  liveStatusThinking = existingModelMatch?.[2] || null;
  reconnectBanner = document.querySelector(".session-reconnect");
  updateNotificationToggle();
  gatewayUpdateController.apply();
}

function editableElement(element) {
  return element?.closest?.("input, textarea, select, [contenteditable]");
}

function sessionSwitching() {
  return document.body.classList.contains("session-switching");
}

function blockSessionSwitchingKeyboard(event) {
  if (!sessionSwitching()) return;

  event.preventDefault();
  event.stopImmediatePropagation();
}

function currentSessionFindShortcut(event) {
  if (!currentSessionFindController.available || String(event.key || "").toLowerCase() !== "f") return false;
  if (event.altKey || event.shiftKey) return false;
  return !!(event.ctrlKey || event.metaKey);
}

function requestSessionSearch() {
  if (sessionSwitching() || modalIsOpen()) return false;
  return sidebarController.openSearch();
}

function handleSessionSearchShortcut(event) {
  if (!sessionSearchShortcut(event) || !requestSessionSearch()) return false;
  event.preventDefault();
  return true;
}

// Each command calls what its own control calls. Commands that Pi runs through the composer stay under "/", where they cannot take a draft with them.
function commandPaletteCommands() {
  const row = sidebarController.element?.querySelector('.session-row[data-current="true"]');
  const target = row && sessionActionsController.targetFor(row);
  const writable = promptTextarea && !promptTextarea.disabled;
  const otherView = document.querySelector('[data-conversation-view][aria-pressed="false"]');
  const sidebarHidden = document.body.classList.contains("desktop-sidebar-hidden");
  return [
    ["This session", [
      writable && { label: "Choose model and thinking", detail: "/model", run: openModelSettingsModal },
      writable && { label: "Session tree", detail: "/tree", run: openTreeSessionModal },
      writable && { label: "Fork from a message", detail: "/fork", run: openForkSessionModal },
      target && {
        label: "Rename…",
        run: () => {
          // No row menu opened this, so closing it has no menu button to return the focus to.
          sessionActionsController.target = null;
          sessionActionsController.openRename(target);
        }
      },
      // Anchored to the composer, which gets the focus back.
      target && { label: "Tags…", run: () => sessionTagsController.open(target.path, promptTextarea) },
      target && { label: target.pinned ? "Unpin" : "Pin", run: () => sessionActionsController.togglePin(target).catch(() => {}) },
      currentSessionFindController.available && { label: "Find in session", keys: "ctrl+f", run: requestCurrentSessionFind },
      otherView && { label: `${otherView.dataset.conversationView === "brief" ? "Brief" : "Full"} activity`, run: () => otherView.click() }
    ]],
    ["Gripi", [
      { label: "New session…", keys: "ctrl+n", run: openNewSessionModal },
      { label: "Environment…", run: () => environmentController.open() },
      // On narrow screens the sidebar is a drawer instead.
      window.matchMedia("(min-width: 761px)").matches && { label: sidebarHidden ? "Show sidebar" : "Hide sidebar", run: () => sidebarController.setDesktopVisibility(!sidebarHidden, true) }
    ]]
  ];
}

function requestCurrentSessionFindNavigation(direction) {
  if (sessionSwitching() || modalIsOpen() || !currentSessionFindController.open) return false;
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
  if (sessionSwitching() || modalIsOpen() || !currentSessionFindController.available) return false;
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

  const agentBusy = ["running", "sending", "exporting"].includes(state);
  if (!agentBusy && !conversationController.nearBottom()) return;

  const target = agentBusy ? conversationScroll : promptTextarea;
  target?.focus({ preventScroll: true });
}

function toggleConversationPromptFocus(event, nextElement) {
  if (!nextElement || window.matchMedia?.("(pointer: fine)").matches === false) return false;
  event.preventDefault();
  nextElement.focus({ preventScroll: true });
  return true;
}

function selectedStreamingBehavior() {
  return streamingBehaviorSelection;
}

function submittedStreamingBehavior() {
  const override = keyboardStreamingBehaviorOverride;
  keyboardStreamingBehaviorOverride = null;
  if (composerState?.dataset.state !== "running") return null;
  return override || selectedStreamingBehavior();
}

function closeSendMenu(focusTarget = undefined) {
  const activeElementWillBeHidden = sendMenu?.contains(document.activeElement) || (sendMenuToggle?.hidden && document.activeElement === sendMenuToggle);
  if (sendMenu) sendMenu.hidden = true;
  sendMenuToggle?.setAttribute("aria-expanded", "false");
  const defaultFocusTarget = activeElementWillBeHidden ? (composerState?.dataset.state === "running" ? sendButton : promptTextarea) : null;
  (focusTarget === undefined ? defaultFocusTarget : focusTarget)?.focus();
}

function selectStreamingBehavior(behavior, { focus = true } = {}) {
  if (!["steer", "follow_up"].includes(behavior)) return;
  streamingBehaviorSelection = behavior;
  closeSendMenu(focus ? promptTextarea : null);
  updateStreamingSendControl();
  updatePromptPlaceholder();
}

function updateStreamingSendControl(state = composerState?.dataset.state) {
  const running = state === "running";
  const behavior = selectedStreamingBehavior();
  if (sendControl) sendControl.classList.toggle("is-streaming", running);
  if (sendMenuToggle) sendMenuToggle.hidden = !running;
  sendMenu?.querySelectorAll("[data-streaming-behavior]").forEach((button) => button.setAttribute("aria-pressed", String(button.dataset.streamingBehavior === behavior)));
  if (sendButton && running) {
    sendButton.textContent = behavior === "follow_up" ? "Queue" : "Steer";
    sendButton.setAttribute("aria-label", behavior === "follow_up" ? "Queue follow-up" : "Send steer");
  }
  if (!running) closeSendMenu();
}

function updatePromptPlaceholder() {
  if (!promptTextarea) return;
  if (sessionSyncBlocked()) {
    promptTextarea.placeholder = "Sending is paused.";
    return;
  }
  if (composerState?.dataset.state === "running") {
    promptTextarea.placeholder = selectedStreamingBehavior() === "follow_up" ? "Queue follow-up…" : "Steer Pi…";
    return;
  }
  if (promptTextarea.disabled) {
    promptTextarea.placeholder = composerState?.dataset.state === "exporting" ? "Exporting…" : "Sending…";
    return;
  }
  promptTextarea.placeholder = "Ask Pi…";
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
      item.disabled = ["running", "sending", "exporting"].includes(composerState?.dataset.state);
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
  promptForm?.setAttribute("data-thinking-level", liveStatusThinking || "");
  if (!liveStatusModel) {
    removeStatusItem("thinking");
    return;
  }

  setStatusItem("model", "Model", [liveStatusModel, liveStatusThinking ? `(${liveStatusThinking})` : null].filter(Boolean).join(" "));
  removeStatusItem("thinking");
}

function syncModelSettingsControls() {
  const controls = document.querySelector("[data-model-picker-controls]");
  if (controls) controls.disabled = modelSettingsPending || ["sending", "exporting", "stopping"].includes(composerState?.dataset.state);
}

function setModelSettingsStatus(message, error = false) {
  const status = document.querySelector("[data-model-settings-status]");
  if (!status) return;
  status.textContent = message;
  status.classList.toggle("is-error", error);
}

function renderThinkingOptions() {
  const container = document.querySelector("[data-thinking-levels]");
  const line = document.querySelector("[data-thinking-options]");
  if (!container || !line) return;
  const model = modelSettingsCurrentModel;
  container.replaceChildren();
  line.hidden = !model;
  if (!model) return;
  supportedThinkingLevels(model).forEach((level, index) => {
    if (index) {
      const separator = document.createElement("span");
      separator.className = "picker-separator";
      separator.setAttribute("aria-hidden", "true");
      separator.textContent = "|";
      container.append(separator);
    }
    const button = document.createElement("button");
    button.type = "button";
    button.textContent = level;
    button.setAttribute("aria-pressed", String(level === modelSettingsCurrentThinking));
    button.addEventListener("click", () => { applyModelSettings(model, level).catch(() => {}); });
    container.append(button);
  });
}

function setModelSettingsCursor(index) {
  const list = document.querySelector("[data-model-list]");
  const model = modelSettingsVisibleModels[index];
  if (!list || !model) return;
  modelSettingsActiveIndex = index;
  [...list.children].forEach((row, rowIndex) => row.setAttribute("aria-selected", String(rowIndex === index)));
  list.children[index].scrollIntoView({ block: "nearest" });
  document.querySelector("[data-model-search]")?.setAttribute("aria-activedescendant", list.children[index].id);
  const count = modelSettingsVisibleModels.length;
  // Like Pi CLI, show the position only once the list is too long to see at a glance.
  setModelSettingsStatus(`Model Name: ${model.name || model.id}${count > 10 ? ` (${index + 1}/${count})` : ""}`);
}

function renderModelSettingsModels() {
  const list = document.querySelector("[data-model-list]");
  const search = document.querySelector("[data-model-search]");
  if (!list) return;
  const query = search?.value.trim() || "";
  const currentKey = modelSettingsKey(modelSettingsCurrentModel || {});
  modelSettingsVisibleModels = matchingPickerModels(modelSettingsScope === "scoped" ? modelSettingsScopedModels : modelSettingsModels, query);
  list.replaceChildren(...modelSettingsVisibleModels.map((model, index) => {
    const row = document.createElement("button");
    row.type = "button";
    row.className = "picker-row model-picker-row";
    row.id = `model-picker-option-${index}`;
    row.tabIndex = -1;
    row.setAttribute("role", "option");
    if (modelSettingsKey(model) === currentKey) row.setAttribute("aria-current", "true");
    const cursor = document.createElement("span");
    cursor.className = "picker-cursor";
    cursor.textContent = "→";
    const check = document.createElement("span");
    check.className = "picker-check";
    check.textContent = "✓";
    cursor.setAttribute("aria-hidden", "true");
    check.setAttribute("aria-hidden", "true");
    const provider = document.createElement("span");
    provider.className = "model-picker-provider";
    provider.textContent = `[${model.provider || "unknown"}]`;
    const label = document.createElement("span");
    label.append(model.id || "Unknown model", " ", provider);
    row.append(cursor, check, label);
    row.addEventListener("click", () => { applyModelSettings(model, selectedThinkingLevel(model, modelSettingsCurrentThinking)).catch(() => {}); });
    return row;
  }));
  search?.removeAttribute("aria-activedescendant");
  if (!modelSettingsVisibleModels.length) {
    setModelSettingsStatus("No matching models");
    return;
  }
  // Like Pi CLI: a search starts at its first match, otherwise the cursor rests on the current model.
  setModelSettingsCursor(query ? 0 : Math.max(0, modelSettingsVisibleModels.findIndex((model) => modelSettingsKey(model) === currentKey)));
}

function setModelSettingsScope(scope) {
  modelSettingsScope = scope;
  const line = document.querySelector("[data-model-scope]");
  if (line) line.hidden = !modelSettingsScopedModels.length;
  document.querySelectorAll("[data-model-scope-option]").forEach((button) => {
    button.setAttribute("aria-pressed", String(button.dataset.modelScopeOption === scope));
  });
  renderModelSettingsModels();
}

async function loadModelSettings(modal, operation) {
  const sessionPath = currentSessionPath();
  if (!modal || !sessionPath) return;
  modelSettingsModels = [];
  modelSettingsScopedModels = [];
  modelSettingsCurrentModel = null;
  setModelSettingsScope("all");
  renderThinkingOptions();
  modelSettingsPending = true;
  syncModelSettingsControls();
  setModelSettingsStatus("Loading models…");
  try {
    const response = await fetch(`/sessions/model_settings?session=${encodeURIComponent(sessionPath)}`, { headers: { "Accept": "application/json" } });
    const payload = await response.json().catch(() => null);
    if (!response.ok || !payload) throw new Error(payload?.error || "Could not load models.");
    if (operation !== modelSettingsOperationGeneration || modal.hidden || sessionPath !== currentSessionPath()) return;
    const models = Array.isArray(payload.models) ? payload.models : [];
    modelSettingsCurrentModel = payload.state?.model || null;
    modelSettingsCurrentThinking = payload.state?.thinkingLevel || "off";
    modelSettingsModels = sortedPickerModels(models, modelSettingsCurrentModel);
    modelSettingsScopedModels = scopedPickerModels(models, payload.scopedModels);
    modelSettingsPending = false;
    syncModelSettingsControls();
    setModelSettingsScope(modelSettingsScopedModels.length ? "scoped" : "all");
    renderThinkingOptions();
  } catch (error) {
    if (operation === modelSettingsOperationGeneration && !modal.hidden && sessionPath === currentSessionPath()) {
      setModelSettingsStatus(error.message || "Could not load models.", true);
    }
  }
}

function openModelSettingsModal() {
  if (["sending", "exporting", "stopping"].includes(composerState?.dataset.state)) return false;
  const modal = document.querySelector('[data-modal="model-settings-modal"]');
  const search = modal?.querySelector("[data-model-search]");
  if (search) search.value = "";
  const operation = ++modelSettingsOperationGeneration;
  openModal(modal);
  // On touch screens focusing the search would open the keyboard over the list.
  if (automaticComposerFocusEnabled()) search?.focus();
  loadModelSettings(modal, operation).catch(() => {});
  return !!modal;
}

async function applyModelSettings(model, thinking) {
  const modal = document.querySelector('[data-modal="model-settings-modal"]');
  const sessionPath = currentSessionPath();
  if (!modal || !sessionPath) return;
  if (modelSettingsKey(model) === modelSettingsKey(modelSettingsCurrentModel || {}) && thinking === modelSettingsCurrentThinking) {
    closeModal(modal);
    return;
  }
  const operation = ++modelSettingsOperationGeneration;
  const formData = new FormData();
  formData.set("session", sessionPath);
  formData.set("provider", model.provider || "");
  formData.set("model", model.id || "");
  formData.set("thinking", thinking);
  modelSettingsPending = true;
  syncModelSettingsControls();
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
      modelSettingsPending = false;
      syncModelSettingsControls();
      setModelSettingsStatus(error.message || "Could not apply model settings.", true);
    }
  }
}

function handleModelSettingsKey(event) {
  if (document.querySelector('[data-modal="model-settings-modal"]')?.hidden !== false) return;
  if (event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey) return;
  const inSearch = event.target.matches?.("[data-model-search]");
  const count = modelSettingsVisibleModels.length;
  if (event.key === "ArrowDown" || event.key === "ArrowUp") {
    event.preventDefault();
    if (count) setModelSettingsCursor((modelSettingsActiveIndex + (event.key === "ArrowDown" ? 1 : count - 1)) % count);
  } else if (event.key === "Enter" && inSearch && !event.isComposing) {
    event.preventDefault();
    document.querySelector('[data-model-list] [aria-selected="true"]')?.click();
  } else if (event.key === "Tab" && !event.shiftKey && inSearch && modelSettingsScopedModels.length) {
    // Pi CLI's /model switches scope with Tab; Shift+Tab still moves focus.
    event.preventDefault();
    setModelSettingsScope(modelSettingsScope === "scoped" ? "all" : "scoped");
  }
}

function updateStatusFromMessage(message) {
  if (message?.provider || message?.model) {
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
  if (notificationsDisabled()) return false;
  if (desktopNotificationAvailable()) return true;
  if (webPushController.available()) return webPushEnabled;
  return ("Notification" in window) && Notification.permission === "granted";
}

function notificationToggleState() {
  if (notificationsDisabled()) return { name: "off", title: "Notifications off — click to enable" };
  if (notificationsEnabled()) return { name: "enabled", title: "Notifications on — click to disable" };
  if (!desktopNotificationAvailable() && ("Notification" in window) && Notification.permission === "denied") return { name: "blocked", title: "Notifications blocked — click for setup help" };
  return { name: "enable", title: "Enable notifications" };
}

function updateNotificationToggle() {
  const toggle = document.querySelector("[data-notification-toggle]");
  if (!toggle) return;

  const state = notificationToggleState();
  toggle.classList.toggle("is-enabled", state.name === "enabled");
  toggle.classList.toggle("is-blocked", state.name === "blocked");
  toggle.title = state.title;
  toggle.setAttribute("aria-label", state.title);
}

async function toggleNotifications() {
  if (notificationsEnabled()) {
    localStorage.setItem("gripi:notifications-disabled", "true");
    webPushEnabled = false;
    updateNotificationToggle();
    if (webPushController.available()) await webPushController.disable();
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

  if (webPushController.available()) {
    webPushEnabled = await webPushController.enable();
  } else if (Notification.permission === "default") {
    await Notification.requestPermission();
  }
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
    await worker.showNotification(title, { body, tag, renotify: true, icon: "/app-icon.svg", badge: "/assets/notification-badge.png", data: { url } });
  }
}

function sessionIsActivelyViewed(sessionPath) {
  return sessionPath && sessionPath === currentSessionPath() && !document.hidden && document.hasFocus();
}

function finalAssistantReplyKey(sessionPath, message) {
  const text = liveMessageParser.messageText(message);
  return [sessionPath, message.id || message.messageId || message.responseId || message.timestamp, stableTextHash(text)].join(":");
}

function notifyFinalAssistantReply(message) {
  if (liveOutput?.dataset.sessionSyncMode === "external_follow" || webPushController.enabled()) return;
  if (!message || (typeof message.stopReason === "string" && !["", "stop", "length"].includes(message.stopReason))) return;
  if (!liveMessageParser.finalAssistantReplySegments(message).length) return;

  const sessionPath = currentSessionPath();
  if (!sessionPath) return;
  if (sessionIsActivelyViewed(sessionPath)) return;

  const key = finalAssistantReplyKey(sessionPath, message);
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

  const requestVersion = ++sessionStatusRequestVersion;
  const response = await fetch(statusUrl);
  if (!response.ok || requestVersion !== sessionStatusRequestVersion || generation !== sessionViewGeneration || statusBar !== sessionStatusBar) return;
  const status = await response.json();
  if (requestVersion !== sessionStatusRequestVersion || generation !== sessionViewGeneration || statusBar !== sessionStatusBar) return;
  setStatusItem("ctx", "CTX", status.context);
  liveStatusModel = status.model;
  liveStatusThinking = status.thinking;
  renderModelStatus();
}

function updateWaitingForOutputStatus() {
  if (!composerState) return;
  if (["running", "bash"].includes(composerState.dataset.state) && Date.now() <= escapeStopConfirmationExpiresAt) {
    composerState.textContent = "Press ESC again to stop current task";
    return;
  }
  if (composerState.dataset.state === "bash") {
    composerState.textContent = "Shell command running…";
    return;
  }
  if (composerState.dataset.state !== "running" || !waitingForOutputSince) return;

  const elapsed = Date.now() - waitingForOutputSince;
  composerState.textContent = `${waitingForOutputLabel} ${formatWaitDuration(elapsed)}`;
}

function startWaitingForOutput(since = Date.now()) {
  waitingForOutputSince = since || Date.now();
  clearInterval(waitingForOutputTimer);
  updateWaitingForOutputStatus();
  waitingForOutputTimer = setInterval(updateWaitingForOutputStatus, 1000);
}

function syncComposerSpinner() {
  if (!["running", "bash", "sending", "exporting"].includes(composerState?.dataset.state)) {
    clearInterval(composerSpinnerTimer);
    composerSpinnerTimer = null;
    delete composerState?.dataset.spinner;
    return;
  }
  composerState.dataset.spinner = COMPOSER_SPINNER_FRAMES[composerSpinnerFrame];
  if (composerSpinnerTimer || window.matchMedia?.("(prefers-reduced-motion: reduce)").matches) return;
  composerSpinnerTimer = setInterval(() => {
    composerSpinnerFrame = (composerSpinnerFrame + 1) % COMPOSER_SPINNER_FRAMES.length;
    syncComposerSpinner();
  }, 80);
}

function stopWaitingForOutput() {
  waitingForOutputSince = null;
  clearInterval(waitingForOutputTimer);
  waitingForOutputTimer = null;
}

function sessionSyncBlocked() {
  return ["external_follow", "conflict"].includes(liveOutput?.dataset.sessionSyncMode);
}

function showCurrentActiveTask(idleState = "done", idleLabel = "Done") {
  const compacting = liveOutput?.dataset.composerCompacting === "true";
  if (liveAgentRunning || compacting) {
    setComposerState("running", compacting ? "Compacting…" : WORKING_LABEL, { since: liveBusySince });
  } else if (liveBash) {
    setComposerState("bash", "Shell command running…");
  } else {
    setComposerState(idleState, idleLabel);
  }
}

function setComposerState(state, label = "", { since = null, focus = true } = {}) {
  const previousState = composerState?.dataset.state;
  const sessionPath = currentSessionPath();
  if (state === "stopping") stoppingSessionPaths.add(sessionPath);
  if (state === "running" && stoppingSessionPaths.has(sessionPath)) return;
  if (!["running", "stopping"].includes(state)) stoppingSessionPaths.delete(sessionPath);
  if (state === "running") waitingForOutputLabel = label || WORKING_LABEL;
  if (state === "running" && (since || !waitingForOutputSince)) startWaitingForOutput(since || Date.now());
  if (!["running", "bash"].includes(state)) {
    escapeStopConfirmationExpiresAt = 0;
    clearTimeout(escapeStopConfirmationTimer);
    escapeStopConfirmationTimer = null;
  }
  if (!["running", "sending"].includes(state)) stopWaitingForOutput();
  if (composerState) {
    composerState.dataset.state = state;
    composerState.textContent = ["running", "bash", "sending", "exporting", "stopping", "error", "success"].includes(state) ? label : "";
    if (state === "running") updateWaitingForOutputStatus();
    syncComposerSpinner();
  }
  const activeTask = liveAgentRunning || liveOutput?.dataset.composerCompacting === "true";
  const taskBusy = ["running", "bash", "sending", "stopping"].includes(state) || (state === "exporting" && activeTask);
  const submitting = ["sending", "exporting"].includes(state);
  const stopping = state === "stopping";
  if (!["running", "sending"].includes(state)) streamingBehaviorSelection = "steer";
  updateStreamingSendControl(state);
  if (abortButton) abortButton.disabled = !taskBusy || stopping;
  if (sendButton) {
    sendButton.hidden = submitting || stopping;
    sendButton.disabled = stopping || sessionSyncBlocked();
    if (state !== "running") {
      sendButton.textContent = state === "sending" ? "Sending…" : "Send";
      sendButton.setAttribute("aria-label", "Send message");
    }
  }
  if (composerStopButton) {
    composerStopButton.hidden = !taskBusy;
    composerStopButton.disabled = !taskBusy || stopping;
    composerStopButton.classList.toggle("is-visible", taskBusy);
  }
  if (promptTextarea) promptTextarea.disabled = submitting || stopping || sessionSyncBlocked();
  if (focus && state !== previousState) syncComposerFocus(state);
  const modelButton = sessionStatusBar?.querySelector('[data-status-key="model"]');
  if (modelButton) modelButton.disabled = submitting || stopping || sessionSyncBlocked();
  syncModelSettingsControls();
  if (composerState && state === "running" && previousState !== "running") {
    resetEventPollBackoff();
    scheduleNextEventPoll(0);
    sidebarController.requestRefresh();
  }
  const attachmentsDisabled = submitting || stopping || sessionSyncBlocked();
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

  // Like Pi CLI's editor, switch to bash mode as soon as the text starts with "!".
  promptForm?.toggleAttribute("data-bash-mode", promptTextarea.value.trimStart().startsWith("!"));
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
  if (typeof baseDocumentTitle !== "undefined") {
    baseDocumentTitle = `${name} · Gripi`;
    renderDocumentTitle();
  } else {
    document.title = `${name} · Gripi`;
  }
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
  if (!restore && promptTextarea?.disabled) return false;

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

function extensionUiResponseBody(request, extra = {}) {
  if (!request?.id || !request.session || request.generation !== sessionViewGeneration || request.session !== currentSessionPath()) return null;
  const body = new URLSearchParams({ session: request.session, id: request.id, ...extra });
  addSessionViewFormParams(body);
  return body;
}

function setExtensionUiDeliveryPending(pending) {
  extensionUiDeliveryPending = pending;
  extensionUiModal?.querySelectorAll("button, input, textarea").forEach((control) => { control.disabled = pending; });
}

function showExtensionUiError(message) {
  if (!extensionUiError) return;
  extensionUiError.textContent = message;
  extensionUiError.hidden = false;
}

async function sendExtensionUiResponse(extra = {}) {
  const request = activeExtensionUiRequest;
  if (!request || extensionUiDeliveryPending) return false;
  if (extensionUiRequestExpired(request)) {
    finishExtensionUiRequest(request);
    return false;
  }

  const body = extensionUiResponseBody(request, extra);
  if (!body) return false;
  setExtensionUiDeliveryPending(true);
  if (extensionUiError) extensionUiError.hidden = true;
  try {
    const response = await fetch("/extension_ui_response", { method: "POST", body, headers: { "Accept": "application/json" } });
    const disposition = extensionUiResponseDisposition(response);
    if (disposition === "definitive-rejection") {
      if (activeExtensionUiRequest === request) finishExtensionUiRequest(request);
      return false;
    }
    if (disposition === "retry") throw new Error("Extension UI response was rejected");
    if (activeExtensionUiRequest === request) finishExtensionUiRequest(request);
    return true;
  } catch (_error) {
    if (activeExtensionUiRequest === request && !extensionUiRequestExpired(request)) {
      setExtensionUiDeliveryPending(false);
      showExtensionUiError("Could not answer extension request. Please try again.");
      showStatus("Could not answer extension request", true);
    }
    return false;
  }
}

function resetExtensionUiModal() {
  if (!extensionUiModal) return;
  if (extensionUiTitle) extensionUiTitle.textContent = "Extension request";
  if (extensionUiMessage) {
    extensionUiMessage.textContent = "";
    extensionUiMessage.hidden = true;
  }
  if (extensionUiError) {
    extensionUiError.textContent = "";
    extensionUiError.hidden = true;
  }
  if (extensionUiOptions) {
    extensionUiOptions.replaceChildren();
    extensionUiOptions.hidden = true;
  }
  [extensionUiInput, extensionUiEditor].forEach((field) => {
    if (!field) return;
    field.hidden = true;
    field.value = "";
  });
  if (extensionUiSubmit) extensionUiSubmit.hidden = false;
  setExtensionUiDeliveryPending(false);
}

function finishExtensionUiRequest(request) {
  if (activeExtensionUiRequest !== request) return;
  clearTimeout(extensionUiTimeoutTimer);
  extensionUiTimeoutTimer = null;
  closeModal(extensionUiModal);
  activeExtensionUiRequest = null;
  resetExtensionUiModal();
  showNextExtensionUiDialog();
}

async function cancelExtensionUiRequest() {
  if (!activeExtensionUiRequest) return;
  await sendExtensionUiResponse({ cancelled: "true" });
}

function showNextExtensionUiDialog() {
  if (activeExtensionUiRequest) return;
  while (extensionUiRequestQueue.length > 0 && extensionUiRequestExpired(extensionUiRequestQueue[0])) extensionUiRequestQueue.shift();
  const request = extensionUiRequestQueue.shift();
  if (!request) return;
  activeExtensionUiRequest = request;
  openExtensionUiDialog(request);
  if (request.expiresAt) {
    extensionUiTimeoutTimer = setTimeout(() => finishExtensionUiRequest(request), Math.max(0, request.expiresAt - Date.now()));
  }
}

function enqueueExtensionUiDialog(event) {
  if (!extensionUiModal || !event.id || !["select", "confirm", "input", "editor"].includes(event.method)) return false;
  if (activeExtensionUiRequest?.id === event.id || extensionUiRequestQueue.some((request) => request.id === event.id)) return true;
  const timeout = Number(event.timeout);
  const request = {
    ...event,
    session: currentSessionPath(),
    generation: sessionViewGeneration,
    expiresAt: Number.isFinite(timeout) && timeout > 0 ? Date.now() + timeout : null
  };
  if (!extensionUiRequestExpired(request)) extensionUiRequestQueue.push(request);
  showNextExtensionUiDialog();
  return true;
}

function openExtensionUiDialog(event) {
  resetExtensionUiModal();
  const title = event.title || "Extension request";
  if (extensionUiTitle) extensionUiTitle.textContent = title;
  if (extensionUiMessage && event.message) {
    extensionUiMessage.textContent = event.message;
    extensionUiMessage.hidden = false;
  }

  // Like Pi CLI, a confirmation is a Yes/No selector.
  const choices = event.method === "select"
    ? (Array.isArray(event.options) ? event.options : []).map((option) => ({ label: String(option), response: { value: String(option) } }))
    : event.method === "confirm"
      ? [{ label: "Yes", response: { confirmed: "true" } }, { label: "No", response: { confirmed: "false" } }]
      : null;
  const field = event.method === "input" ? extensionUiInput : extensionUiEditor;
  if (choices) {
    choices.forEach(({ label, response }) => {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "picker-row picker-option";
      button.setAttribute("role", "option");
      const cursor = document.createElement("span");
      cursor.className = "picker-cursor";
      cursor.setAttribute("aria-hidden", "true");
      cursor.textContent = "→";
      const text = document.createElement("span");
      text.textContent = label;
      button.append(cursor, text);
      button.addEventListener("click", () => { sendExtensionUiResponse(response); });
      extensionUiOptions.append(button);
    });
    extensionUiOptions.hidden = false;
    extensionUiSubmit.hidden = true;
  } else {
    field.hidden = false;
    field.placeholder = event.placeholder || "";
    field.value = event.value || event.prefill || "";
  }
  extensionUiHint.textContent = choices
    ? "↑↓ navigate · enter select · esc cancel"
    : `enter submit${event.method === "editor" ? " · shift+enter newline" : ""} · esc cancel`;

  openModal(extensionUiModal);
  if (choices) movePickerCursor(extensionUiOptions, 0);
  else field.focus();
}

async function submitExtensionUiDialog(event) {
  event.preventDefault();
  const method = activeExtensionUiRequest?.method;
  if (method === "input") await sendExtensionUiResponse({ value: extensionUiInput.value });
  else if (method === "editor") await sendExtensionUiResponse({ value: extensionUiEditor.value });
}

function handleExtensionEditorText(event) {
  if (!promptTextarea) return;
  const text = event.text || "";
  if (!promptTextarea.value) {
    promptTextarea.value = text;
    resizePromptTextarea();
    syncComposerFocus();
    showStatus("Extension updated the editor");
    return;
  }
  if (window.confirm("Extension wants to replace your draft. Apply it?")) {
    promptTextarea.value = text;
    resizePromptTextarea();
    syncComposerFocus();
    showStatus("Extension updated the editor");
  } else {
    showStatus("Kept existing draft");
  }
}

function renderDocumentTitle() {
  document.title = extensionDocumentTitle ?? baseDocumentTitle;
}

function updateExtensionWidget(event) {
  if (!event.widgetKey) return;
  if (!Array.isArray(event.widgetLines)) extensionWidgets.delete(event.widgetKey);
  else extensionWidgets.set(event.widgetKey, { lines: event.widgetLines.map((line) => String(line)), placement: event.widgetPlacement || "aboveEditor" });
  renderExtensionWidgets();
}

function resetExtensionUiState() {
  clearTimeout(extensionUiTimeoutTimer);
  extensionUiTimeoutTimer = null;
  extensionUiRequestQueue = [];
  activeExtensionUiRequest = null;
  closeModal(extensionUiModal);
  resetExtensionUiModal();
  extensionWidgets.clear();
  extensionDocumentTitle = null;
  renderExtensionWidgets();
  renderDocumentTitle();
}

function hydrateExtensionUiState() {
  extensionWidgets.clear();
  extensionDocumentTitle = null;
  let state = {};
  try {
    state = JSON.parse(liveOutput?.dataset.extensionUiState || "{}");
  } catch (_error) {
  }
  (Array.isArray(state.widgets) ? state.widgets : []).forEach(updateExtensionWidget);
  renderExtensionWidgets();
  extensionDocumentTitle = state.title?.title == null ? null : state.title.title;
  renderDocumentTitle();
  (Array.isArray(state.pending_dialogs) ? state.pending_dialogs : []).forEach(enqueueExtensionUiDialog);
}

function renderExtensionWidgets() {
  if (!extensionWidgetsAboveContainer || !extensionWidgetsBelowContainer) return;
  extensionWidgetsAboveContainer.replaceChildren();
  extensionWidgetsBelowContainer.replaceChildren();
  extensionWidgets.forEach((widget, key) => {
    const block = document.createElement("div");
    block.className = "extension-widget";
    block.dataset.extensionWidgetKey = key;
    const title = document.createElement("span");
    title.className = "extension-widget-title";
    title.textContent = key;
    const body = document.createElement("div");
    body.textContent = widget.lines.join("\n");
    block.append(title, body);
    (widget.placement === "belowEditor" ? extensionWidgetsBelowContainer : extensionWidgetsAboveContainer).append(block);
  });
  extensionWidgetsAboveContainer.hidden = extensionWidgetsAboveContainer.children.length === 0;
  extensionWidgetsBelowContainer.hidden = extensionWidgetsBelowContainer.children.length === 0;
}

function eventTimeMilliseconds(event) {
  const value = eventTimestamp(event);
  const timestamp = typeof value === "number" ? value : Date.parse(value);
  return Number.isFinite(timestamp) ? timestamp : Date.now();
}

function renderErrorEvent(event) {
  const errorText = eventErrorText(event);
  if (!errorText) return false;
  liveMessageRenderer.clearActiveActivity();
  liveErrorText = errorText;
  liveMessageRenderer.appendMessage("error", errorText, true, true, eventTimestamp(event));
  showStatus(errorText, true);
  showCurrentActiveTask("error", errorText);
  return true;
}

function startLiveBash(event) {
  if (liveMessageRenderer.bashExecutionCompleted(event.bashId)) return;
  const startedAt = eventTimeMilliseconds(event);
  liveBash = {
    id: event.bashId,
    command: event.command,
    excludeFromContext: event.excludeFromContext === true,
    startedAt
  };
  liveMessageRenderer.renderBashEvent(event);
  if (!liveAgentRunning && liveOutput?.dataset.composerCompacting !== "true" && !["sending", "exporting", "stopping"].includes(composerState?.dataset.state)) {
    setComposerState("bash", "Shell command running…");
  }
  resetEventPollBackoff();
}

function finishLiveBash(event) {
  if (liveMessageRenderer.bashExecutionCompleted(event.bashId)) return;
  liveMessageRenderer.renderBashEvent(event);
  stoppingSessionPaths.delete(currentSessionPath());
  if (liveBash?.id === event.bashId) liveBash = null;
  if (!["sending", "exporting"].includes(composerState?.dataset.state)) {
    showCurrentActiveTask("done", event.type === "bash_error" ? "Shell command failed" : "Done");
  }
}

function renderEvent(event) {
  if (["agent_start", "turn_start"].includes(event.type)) conversationController.setActivityRunning(true);
  if (["agent_end", "agent_settled"].includes(event.type)) conversationController.setActivityRunning(false);
  if (["agent_start", "agent_end", "agent_settled", "turn_end", "compaction_start"].includes(event.type)) liveMessageRenderer.clearActiveActivity();
  const preparingToolCall = event.type === "message_update" && ["toolcall_start", "toolcall_delta"].includes(event.assistantMessageEvent?.type);
  if (["agent_start", "turn_start", "message_start", "message_update", "message_end", "tool_execution_start", "tool_execution_update", "tool_execution_end", "turn_end", "agent_end", "agent_settled", "compaction_start"].includes(event.type) || eventErrorText(event)) {
    if (!preparingToolCall) liveMessageRenderer.setToolPreparation(false);
  }

  if (event.type === "bash_start") {
    startLiveBash(event);
    return;
  }

  if (["bash_end", "bash_error"].includes(event.type)) {
    finishLiveBash(event);
    return;
  }

  if (event.type === "agent_start") {
    pendingFinalAssistantReply = null;
    liveAgentRunning = true;
    liveBusySince = eventTimeMilliseconds(event);
    liveErrorText = "";
    setComposerState("running", WORKING_LABEL, { since: liveBusySince });
    showStatus("Pi is thinking…");
    return;
  }

  if (event.type === "agent_end") {
    pendingFinalAssistantReply = Array.isArray(event.messages) ? event.messages.findLast((message) => message?.role === "assistant") : null;
  }

  if (event.type === "turn_start") {
    liveBusySince ||= eventTimeMilliseconds(event);
    liveErrorText = "";
    setComposerState("running", WORKING_LABEL, { since: liveBusySince });
    showStatus("Pi is thinking…");
    return;
  }

  if (["message_start", "message_update", "message_end"].includes(event.type)) {
    const outcome = liveMessageRenderer.renderMessageEvent(event);
    // Render first: a coalesced tool delta can also carry previously unseen text.
    if (["toolcall_start", "toolcall_delta", "toolcall_end"].includes(event.assistantMessageEvent?.type)) {
      liveMessageRenderer.clearLiveAssistantStreaming();
    }
    if (preparingToolCall) {
      const currentPart = liveMessageParser.eventMessage(event)?.content?.[event.assistantMessageEvent?.contentIndex];
      // Subagent calls are hidden by the parser until tool execution starts.
      const toolCardVisible = currentPart?.type === "toolCall" && currentPart.name !== "subagent";
      liveMessageRenderer.setToolPreparation(!toolCardVisible, eventTimestamp(event));
    }
    if (outcome.finalAssistantEnded) {
      liveOutput.dataset.assistantResponseCount = String(Number(liveOutput.dataset.assistantResponseCount) + 1);
      markCurrentSessionRead();
    }
    if (event.type === "message_end") {
      renderErrorEvent(event);
      refreshSessionStatus().catch(() => {});
    }
    return;
  }

  if (["tool_execution_start", "tool_execution_update", "tool_execution_end"].includes(event.type)) {
    liveMessageRenderer.clearLiveAssistantStreaming();
    liveMessageRenderer.renderToolExecutionEvent(event);
    return;
  }

  if (event.type === "compaction") {
    if (liveOutput) liveOutput.dataset.composerCompacting = "false";
    liveMessageRenderer.renderCompactionEvent(event);
    showStatus("Compaction finished");
    if (!liveAgentRunning) liveBusySince = null;
    showCurrentActiveTask();
    refreshSessionStatus().catch(() => {});
    sidebarController.refresh().catch(() => {});
    return;
  }

  if (event.type === "extension_ui_reset") {
    resetExtensionUiState();
    return;
  }

  if (event.type === "extension_ui_request") {
    if (["select", "confirm", "input", "editor"].includes(event.method)) {
      enqueueExtensionUiDialog(event);
      return;
    }
    if (event.method === "set_editor_text") {
      handleExtensionEditorText(event);
      return;
    }
    // Keep the footer provider-independent; extension statuses may contain raw data.
    if (event.method === "setStatus") return;
    if (event.method === "setTitle") {
      extensionDocumentTitle = event.title == null ? null : event.title;
      renderDocumentTitle();
      return;
    }
    if (event.method === "setWidget") {
      updateExtensionWidget(event);
      return;
    }

    const notice = extensionUiRequestNotice(event);
    if (notice) {
      liveMessageRenderer.appendMessage(notice.role, notice.text, true, true, eventTimestamp(event));
      return;
    }
  }

  if (["custom", "custom_message", "session_info", "session_info_changed", "compaction_start", "compaction_end"].includes(event.type)) {
    updateSessionHeaderName(sessionNameFromEvent(event));
    if (event.type === "custom_message") liveMessageRenderer.renderCustomMessageEvent(event);
    if (["session_info", "session_info_changed"].includes(event.type)) sidebarController.refresh().catch(() => {});
    showStatus(eventStatusText(event));
    if (event.type === "compaction_start") {
      if (liveOutput) liveOutput.dataset.composerCompacting = "true";
      liveMessageRenderer.resetLiveCompactionTracking();
      liveMessageRenderer.removePendingCompactionMessage();
      liveMessageRenderer.appendPendingCompactionMessage(eventTimestamp(event));
      setComposerState("running", "Compacting…", { since: eventTimeMilliseconds(event) });
    }
    if (event.type === "compaction_end") {
      if (liveOutput) liveOutput.dataset.composerCompacting = "false";
      liveMessageRenderer.removePendingCompactionMessage();
      const compactionFailed = !event.aborted && !event.result && event.errorMessage;
      if (compactionFailed) renderErrorEvent(event);
      else if (!event.aborted) liveMessageRenderer.renderCompactionEvent(event);
      if (!compactionFailed) {
        if (!liveAgentRunning) liveBusySince = null;
        showCurrentActiveTask("done", event.aborted ? "Compaction aborted" : "Done");
      }
      if (!event.aborted && !compactionFailed) refreshSessionStatus().catch(() => {});
      sidebarController.refresh().catch(() => {});
    }
    return;
  }

  if (event.type === "turn_end") {
    if (!liveAgentRunning) liveBusySince = null;
    if (!liveErrorText) {
      if (liveMessageRenderer.liveAssistantSeen) showStatus("Done");
      if (!liveAgentRunning) showCurrentActiveTask();
    } else if (!liveAgentRunning) {
      showCurrentActiveTask("error", liveErrorText);
    }
    liveMessageRenderer.clearLiveAssistantStreaming();
    liveMessageRenderer.resetLiveAssistantTracking();
    return;
  }

  if (event.type === "agent_settled") {
    const reply = pendingFinalAssistantReply;
    pendingFinalAssistantReply = null;
    notifyFinalAssistantReply(reply);
    liveAgentRunning = false;
    liveBusySince = null;
    if (renderErrorEvent(event)) {
      liveMessageRenderer.clearLiveAssistantStreaming();
      liveMessageRenderer.resetLiveAssistantTracking();
      return;
    }
    if (!liveErrorText) {
      if (liveMessageRenderer.liveAssistantSeen) showStatus("Done");
      showCurrentActiveTask();
    } else {
      showCurrentActiveTask("error", liveErrorText);
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
  lastEventPollFailed = false;
}

function resetEventCursor() {
  lastEventSeq = Number(liveOutput?.dataset.eventsAfter || 0);
  lastQueueSeq = lastEventSeq;
  queueViewGeneration += 1;
}

function reconcileQueuedMessages(queues, sequence) {
  if (!queues || !Number.isInteger(sequence) || sequence < lastQueueSeq) return false;
  liveMessageRenderer.renderQueuedMessages(queues);
  lastQueueSeq = sequence;
  return true;
}

function scheduleNextEventPoll(delay = nextEventPollDelay()) {
  if (!liveOutput) return;
  clearTimeout(eventPollTimer);
  eventPollTimer = null;
  if (piModalIsOpen()) return;
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

async function refreshCurrentSessionPreservingComposer({ fallbackNavigation = true } = {}) {
  const draft = composerDraft();

  const refreshed = await switchSession(window.location.href, { push: false, focus: false, preserveScroll: true, fallbackNavigation });

  if (refreshed) restoreComposerDraft(draft);
  return refreshed;
}

async function refreshStaleSessionAfterResume(hiddenDuration = 0) {
  if (!liveOutput || document.hidden || sessionSwitching()) return false;
  if (staleSessionRefreshInFlight) return true;

  const pollingGap = Date.now() - lastSessionSyncAt;
  if (hiddenDuration < STALE_SESSION_REFRESH_AFTER_MS && pollingGap < STALE_SESSION_REFRESH_AFTER_MS) return false;

  staleSessionRefreshInFlight = true;
  try {
    return await refreshCurrentSessionPreservingComposer({ fallbackNavigation: false });
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
  if (await refreshStaleSessionAfterResume(hiddenDuration)) return;
  scheduleNextEventPoll(0);
  eventPollResumeTimer = setTimeout(() => {
    // An open modal pauses polling, so a missing sync says nothing about the connection.
    if (!document.hidden && !piModalIsOpen() && lastSessionSyncAt < resumeStartedAt) showReconnectBanner();
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

async function refreshExternalSession(controller, generation) {
  const session = currentSessionPath();
  const switchGeneration = sessionSwitchGeneration.capture();
  const current = () => !controller.signal.aborted && generation === sessionViewGeneration &&
    sessionSwitchGeneration.current(switchGeneration) && session === currentSessionPath();
  const response = await fetch(sessionFragmentUrl(window.location.href), {
    headers: { "Accept": "application/json" }, signal: controller.signal
  });
  if (!response.ok) throw new Error("Session refresh failed");
  const payload = await response.json();
  if (!current()) return;
  if (payload.session !== session) throw new Error("Session changed during refresh");
  const template = document.createElement("template");
  template.innerHTML = payload.conversation_html;
  const snapshot = template.content.querySelector("#conversation-scroll");
  const incomingOutput = snapshot?.querySelector("#live-output");
  if (!incomingOutput) throw new Error("Missing conversation snapshot");
  conversationController.rememberMessageSources(snapshot);
  enhanceMarkdownCodeBlocks(snapshot);
  enhanceMessageLinks(snapshot);
  await liveMessageRenderer.hydrateTerminalOutputs(snapshot, { notify: false });
  if (!current()) return;

  // Capture interaction at apply time, not when the background request started.
  const scrollSnapshot = conversationScrollSnapshot();
  const removed = conversationController.reconcileSnapshot(snapshot);
  removed.forEach((message) => liveMessageRenderer.releaseMessageImageObjectURLs(message));
  liveOutput.replaceChildren();
  Object.assign(liveOutput.dataset, incomingOutput.dataset);
  const banner = template.content.querySelector("[data-session-sync-banner]");
  const previousBanner = promptForm.querySelector("[data-session-sync-banner]");
  const comparableBanner = previousBanner?.cloneNode(true);
  const localError = comparableBanner?.querySelector("[data-session-sync-error]");
  if (localError) {
    localError.textContent = "";
    localError.hidden = true;
  }
  if (comparableBanner?.outerHTML !== banner?.outerHTML) {
    previousBanner?.remove();
    if (banner) promptForm.prepend(banner);
  }
  commandList?.classList.toggle("is-disabled", sessionSyncBlocked());
  if (commandList) {
    if (sessionSyncBlocked()) commandList.dataset.sessionSyncBlocked = "true";
    else delete commandList.dataset.sessionSyncBlocked;
  }
  const clearQueue = document.querySelector("[data-clear-queue]");
  if (clearQueue) clearQueue.disabled = sessionSyncBlocked();
  liveMessageRenderer.bind();
  restoreSessionLiveState({ resetIdleState: true });
  resetEventCursor();
  updateSessionHeaderName(payload.title);
  refreshSessionStatus(generation).catch(() => {});
  if (!restorePreservedConversationScroll(scrollSnapshot)) conversationController.scrollToBottom("auto", { force: true });
  conversationController.updateJumpControls();
  currentSessionFindController.historyChanged();
  sidebarController.requestRefresh();
}

async function pollEvents() {
  if (!liveOutput) return;
  if (sessionSwitching()) {
    scheduleNextEventPoll(250);
    return;
  }
  if (piModalIsOpen()) return;
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
    if (!response.ok || !eventPollCurrent(generation, sessionViewGeneration)) return;

    const payload = await response.json();
    if (!eventPollCurrent(generation, sessionViewGeneration)) return;
    if (!document.hidden && Date.now() - lastSessionSyncAt >= STALE_SESSION_REFRESH_AFTER_MS) {
      const refreshed = await refreshStaleSessionAfterResume();
      if (!refreshed && eventPollCurrent(generation, sessionViewGeneration)) scheduleNextEventPoll(nextEventPollDelay(true));
      return;
    }
    lastSessionSyncAt = Date.now();
    pollSucceeded = true;
    hideReconnectBanner();
    if (sessionSyncRefreshRequired(payload.session_sync) || payload.missed) {
      pendingFinalAssistantReply = null;
      if (sessionSyncBlocked() || ["external_follow", "conflict"].includes(payload.session_sync?.mode)) {
        await refreshExternalSession(controller, generation);
      } else {
        await refreshCurrentSessionPreservingComposer();
      }
      return;
    }
    if (Number.isInteger(payload.last_seq)) {
      lastEventSeq = payload.last_seq;
    }
    emptyEventPollCount = payload.events.length > 0 ? 0 : emptyEventPollCount + 1;
    if (payload.events.length > 0 && composerState?.dataset.state === "running" && !waitingForOutputSince) startWaitingForOutput();
    updateWaitingForOutputStatus();
    // Reconcile the final queue independently: a tool renderer can throw before
    // reaching queue_update, after this batch's global cursor has advanced.
    const queueEvent = payload.events.findLast((event) => event.type === "queue_update");
    if (queueEvent && reconcileQueuedMessages(queueEvent, payload.last_seq)) showStatus(eventStatusText(queueEvent));
    payload.events.forEach((event) => {
      if (event.type === "queue_update") return;
      updateStatusFromEvent(event);
      renderEvent(event);
    });
  } catch (_error) {
    pollSucceeded = false;
    if (!controller.piSuppressedAbort && eventPollCurrent(generation, sessionViewGeneration)) {
      // A single failed poll is usually a blip that the next poll recovers from.
      if (lastEventPollFailed && !document.hidden) showReconnectBanner();
      lastEventPollFailed = true;
    }
  } finally {
    clearTimeout(pollTimeout);
    if (pollSucceeded) lastEventPollFailed = false;
    if (eventPollAbortController === controller) {
      eventPollAbortController = null;
      eventPollInFlight = false;
      if (eventPollCurrent(generation, sessionViewGeneration) && !controller.piSuppressedAbort) scheduleNextEventPoll(nextEventPollDelay(!pollSucceeded));
    }
  }
}

function restoreSubmittedComposerInput(message, imageFiles) {
  if (promptTextarea && message) {
    promptTextarea.value = promptTextarea.value ? `${message}\n${promptTextarea.value}` : message;
    persistStoredComposerDraft();
  }
  imageFiles.forEach((file) => pendingImages.push({ file, url: URL.createObjectURL(file) }));
  renderAttachments();
  resizePromptTextarea();
}

async function submitBashPrompt(rawMessage, bashCommand) {
  const generation = sessionViewGeneration;
  const switchGeneration = sessionSwitchGeneration.capture();
  const submittedSession = promptSessionInput?.value;
  const submittedImageFiles = pendingImages.map((entry) => entry.file);
  const submissionGeneration = ++promptSubmissionGeneration;
  const submittedViewChanged = () => generation !== sessionViewGeneration || !sessionSwitchGeneration.current(switchGeneration) || submittedSession !== promptSessionInput?.value;
  const retryCancelled = () => submittedViewChanged() || submissionGeneration !== promptSubmissionGeneration;
  const formData = new FormData(promptForm);
  addSessionViewFormParams(formData);
  formData.set("message", rawMessage);
  formData.set("bash_mode", "bash");
  submittedImageFiles.forEach((file) => formData.append("images[]", file, file.name || "image"));

  promptTextarea.value = "";
  clearStoredComposerDraft(submittedSession);
  clearAttachments();
  commandList?.classList.remove("is-visible");
  commandList?.removeAttribute("open");
  resetCommandSelection();
  resizePromptTextarea();
  resetEventPollBackoff();
  scheduleNextEventPoll(0);

  const restoreSubmittedBashInput = () => {
    if (submittedViewChanged()) return;
    restoreSubmittedComposerInput(rawMessage, submittedImageFiles);
  };

  try {
    const result = await sendPromptRequest(promptForm.action, formData, {
      retryCancelled,
      onRetry: () => showStatus("Waiting to send…", true)
    });
    if (result.cancelled) return;
    const { response, payload } = result;
    if (submittedViewChanged()) {
      sidebarController.requestRefresh();
      return;
    }
    if (!response.ok || !payload?.bash_id) {
      restoreSubmittedBashInput();
      const errorMessage = payload?.error || (payload?.code === "session_operation_pending" ? "Another session operation is pending. Please retry." : "Shell command failed to start");
      showStatus(errorMessage, true);
      return;
    }

    const completionEvent = {
      type: payload.error ? "bash_error" : "bash_end",
      bashId: payload.bash_id,
      command: bashCommand.command,
      excludeFromContext: bashCommand.excludeFromContext,
      result: payload.data || {},
      error: payload.error,
      gatewayTimestamp: Date.now()
    };
    finishLiveBash(completionEvent);
  } catch (_error) {
    restoreSubmittedBashInput();
    showStatus("Shell command failed to start", true);
  } finally {
    scheduleNextEventPoll(0);
  }
}

async function submitExportPrompt(rawMessage, exportCommand) {
  const activeRun = composerState?.dataset.state === "running";
  const generation = sessionViewGeneration;
  const switchGeneration = sessionSwitchGeneration.capture();
  const submittedSession = promptSessionInput?.value;
  const submittedImageFiles = pendingImages.map((entry) => entry.file);
  const submissionGeneration = ++promptSubmissionGeneration;
  const submittedViewChanged = () => generation !== sessionViewGeneration || !sessionSwitchGeneration.current(switchGeneration) || submittedSession !== promptSessionInput?.value;
  const retryCancelled = () => submittedViewChanged() || submissionGeneration !== promptSubmissionGeneration;
  const formData = new FormData();
  formData.set("session", submittedSession);
  formData.set("filename", exportCommand.filename);

  promptTextarea.value = "";
  clearStoredComposerDraft(submittedSession);
  clearAttachments();
  commandList?.classList.remove("is-visible");
  commandList?.removeAttribute("open");
  resetCommandSelection();
  resizePromptTextarea();
  setComposerState("exporting", "Exporting…");
  showStatus("Exporting session…", true);

  try {
    const result = await sendExportRequest(formData, {
      retryCancelled,
      onRetry: () => {
        setComposerState("exporting", "Waiting to export…");
        showStatus("Waiting to export…", true);
      }
    });
    if (result.cancelled || retryCancelled()) return;
    if (!result.response.ok) throw new Error(result.payload?.error || "Session could not be exported");

    const filename = await downloadResponse(result.response, exportCommand.filename || "pi-session.html", { cancelled: retryCancelled });
    if (!filename || retryCancelled()) return;

    if (composerState?.dataset.state === "exporting") {
      if (activeRun) showCurrentActiveTask();
      else setComposerState("done", "Exported");
    }
    showStatus(`Downloaded ${filename}`, true);
  } catch (error) {
    if (submittedViewChanged()) return;

    restoreSubmittedComposerInput(rawMessage, submittedImageFiles);
    if (composerState?.dataset.state === "exporting") {
      if (activeRun) showCurrentActiveTask();
      else setComposerState("error", error.message || "Session could not be exported");
    }
    showStatus(error.message || "Session could not be exported", true);
  }
}

const PROMPT_RETRY_DELAYS = [250, 500, 1_000];

async function sendExportRequest(formData, { retryCancelled, onRetry }) {
  for (let attempt = 0; ; attempt += 1) {
    const response = await fetch("/sessions/export", { method: "POST", body: formData, headers: { "Accept": "application/json" } });
    if (response.ok) return { response, payload: null };

    const payload = await response.json().catch(() => null);
    const retryDelay = PROMPT_RETRY_DELAYS[attempt];
    const retryable = response.status === 409 && payload?.code === "session_operation_pending" && payload?.retryable !== false && retryDelay !== undefined;
    if (!retryable) return { response, payload };
    if (retryCancelled()) return { cancelled: true };

    onRetry();
    await new Promise((resolve) => setTimeout(resolve, retryDelay));
    if (retryCancelled()) return { cancelled: true };
  }
}

async function sendPromptRequest(action, formData, { retryCancelled, onRetry }) {
  for (let attempt = 0; ; attempt += 1) {
    const response = await fetch(action, { method: "POST", body: formData, headers: { "Accept": "application/json" }, redirect: "manual" });
    if (response.type === "opaqueredirect") return { response, payload: null };

    const payload = await response.json().catch(() => null);
    if (response.ok) return { response, payload };
    const retryDelay = PROMPT_RETRY_DELAYS[attempt];
    const retryable = response.status === 409 && payload?.code === "session_operation_pending" && payload?.retryable !== false && retryDelay !== undefined;
    if (!retryable) return { response, payload };
    if (retryCancelled()) return { cancelled: true };

    onRetry();
    await new Promise((resolve) => setTimeout(resolve, retryDelay));
    if (retryCancelled()) return { cancelled: true };
  }
}

async function submitPrompt(event) {
  event.preventDefault();

  if (promptTextarea?.disabled) return;

  const rawMessage = promptTextarea.value;
  const bashCommand = parseNativeBash(rawMessage);
  if (bashCommand) return submitBashPrompt(rawMessage, bashCommand);

  const streamingBehavior = submittedStreamingBehavior();
  const handleBuiltinCommand = streamingBehavior !== "follow_up";
  const exportCommand = handleBuiltinCommand ? sessionExportSlashCommand(rawMessage) : null;
  if (exportCommand) return submitExportPrompt(rawMessage, exportCommand);

  const queuedPrompt = !!streamingBehavior;
  const followUp = streamingBehavior === "follow_up";
  const steer = streamingBehavior === "steer";
  const compactingQueuedPrompt = queuedPrompt && liveOutput?.dataset.composerCompacting === "true";
  const previousWaitingForOutputSince = waitingForOutputSince;

  const generation = sessionViewGeneration;
  const switchGeneration = sessionSwitchGeneration.capture();
  const submittedSession = promptSessionInput?.value;
  const submittedViewChanged = () => generation !== sessionViewGeneration || !sessionSwitchGeneration.current(switchGeneration) || submittedSession !== promptSessionInput?.value;
  const stopHandlingChangedSubmittedView = () => {
    if (!submittedViewChanged()) return false;

    sidebarController.requestRefresh();
    return true;
  };
  const message = rawMessage.trim();
  const submittedImageFiles = pendingImages.map((entry) => entry.file);
  if (!message && submittedImageFiles.length === 0) return;
  const submissionGeneration = ++promptSubmissionGeneration;
  const submittedPromptSuperseded = () => submissionGeneration !== promptSubmissionGeneration;
  if (streamingBehavior) selectStreamingBehavior("steer", { focus: false });

  const formData = new FormData(promptForm);
  addSessionViewFormParams(formData);
  formData.set("message", message);
  formData.set("bash_mode", "prompt");
  formData.delete("streaming_behavior");
  submittedImageFiles.forEach((file) => formData.append("images[]", file, file.name || "image"));
  if (streamingBehavior) formData.set("streaming_behavior", streamingBehavior);

  const nameCommand = handleBuiltinCommand && sessionNameSlashCommand(message);
  const compactCommand = handleBuiltinCommand && sessionCompactSlashCommand(message);
  const forkCommand = handleBuiltinCommand && sessionForkSlashCommand(message);
  const treeCommand = handleBuiltinCommand && sessionTreeSlashCommand(message);
  const cloneCommand = handleBuiltinCommand && sessionCloneSlashCommand(message);
  const newCommand = handleBuiltinCommand && sessionNewSlashCommand(message);
  const modelCommand = handleBuiltinCommand && sessionModelSlashCommand(message);
  const reloadCommand = handleBuiltinCommand && sessionReloadSlashCommand(message);
  const authGuidanceCommand = handleBuiltinCommand ? sessionAuthGuidanceSlashCommand(message) : null;
  if (!nameCommand && !compactCommand && !forkCommand && !treeCommand && !cloneCommand && !newCommand && !modelCommand && !reloadCommand && !authGuidanceCommand) {
    if (!queuedPrompt) {
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
  setComposerState("sending", nameCommand ? "Naming…" : compactCommand ? "Compacting…" : reloadCommand ? "Reloading…" : cloneCommand ? "Cloning…" : newCommand ? "Starting…" : forkCommand ? "Opening fork…" : treeCommand ? "Opening tree…" : modelCommand ? "Opening model settings…" : authGuidanceCommand ? "Opening instructions…" : compactingQueuedPrompt ? "Queueing for after compaction…" : followUp ? "Queueing follow-up…" : steer ? "Steering…" : "Sending…");
  showStatus(nameCommand ? "Setting session name…" : compactCommand ? "Compacting session…" : reloadCommand ? "Reloading Pi resources…" : cloneCommand ? "Cloning session…" : newCommand ? "Starting new session…" : forkCommand ? "Opening fork picker…" : treeCommand ? "Opening session tree…" : modelCommand ? "Opening model settings…" : authGuidanceCommand ? "Opening authentication instructions…" : compactingQueuedPrompt ? "Queueing for after compaction…" : followUp ? "Queueing follow-up…" : steer ? "Steering Pi…" : "Sending…", true);
  if (cloneCommand || newCommand) showSessionSwitching();

  const restoreSubmittedPromptInput = () => {
    restoreSubmittedComposerInput(message, submittedImageFiles);
    if (cloneCommand || newCommand) hideSessionSwitching();
  };

  const clearPendingCompaction = () => {
    if (!compactCommand) return;

    liveMessageRenderer.removePendingCompactionMessage();
    sidebarController.refresh({ force: true }).catch(() => {});
  };

  const showControlCommandFailure = (message) => {
    restoreSubmittedPromptInput();
    if (reloadCommand && liveBash) setComposerState("bash", "Shell command running…");
    else if (queuedPrompt) showCurrentActiveTask("error", message);
    else setComposerState("error", message);
    showStatus(message, true);
  };

  const showPromptFailure = (errorMessage, { retryableContention = false } = {}) => {
    restoreSubmittedPromptInput();
    clearPendingCompaction();
    if (queuedPrompt) {
      const currentState = composerState?.dataset.state;
      if (["running", "sending"].includes(currentState)) selectStreamingBehavior(streamingBehavior, { focus: false });
      if (currentState === "sending") setComposerState("running", compactingQueuedPrompt ? "Compacting…" : WORKING_LABEL, { since: previousWaitingForOutputSince });
      showStatus(errorMessage, true);
      return;
    }
    if (retryableContention) liveMessageRenderer.removeOptimisticUserMessage(message);
    else liveMessageRenderer.markOptimisticUserMessageFailed(message);
    if (liveBash) showCurrentActiveTask("error", errorMessage);
    else setComposerState("error", errorMessage);
    showStatus(errorMessage, true);
    liveMessageRenderer.appendMessage("assistant", `Prompt failed to send:\n\n${errorMessage}`, true, true, new Date(), { finalAssistantResponse: true, error: true });
  };

  let response;
  let responsePayload;
  try {
    const result = await sendPromptRequest(promptForm.action, formData, {
      retryCancelled: () => stopHandlingChangedSubmittedView() || submittedPromptSuperseded(),
      onRetry: () => {
        setComposerState("sending", "Waiting to send…");
        showStatus("Waiting to send…", true);
      }
    });
    if (result.cancelled) {
      if (!submittedViewChanged()) {
        clearPendingCompaction();
        if (!queuedPrompt) liveMessageRenderer.removeOptimisticUserMessage(message);
      }
      return;
    }
    response = result.response;
    responsePayload = result.payload;
  } catch (_error) {
    if (stopHandlingChangedSubmittedView()) return;
    if (submittedPromptSuperseded()) return;
    if (nameCommand || reloadCommand) {
      showControlCommandFailure(nameCommand ? "Session name could not be changed" : "Pi resources could not be reloaded");
      return;
    }
    showPromptFailure("Prompt failed to send");
    return;
  }
  if (stopHandlingChangedSubmittedView()) return;
  if (submittedPromptSuperseded()) return;

  if (!response.ok && response.type !== "opaqueredirect") {
    const payload = responsePayload;
    if (stopHandlingChangedSubmittedView()) return;
    if (submittedPromptSuperseded()) return;
    if (cloneCommand && payload?.cancelled) {
      restoreSubmittedPromptInput();
      setComposerState("idle");
      showStatus("Clone cancelled", true);
      return;
    }
    if (nameCommand || reloadCommand) {
      showControlCommandFailure(payload?.error || (nameCommand ? "Session name could not be changed" : "Pi resources could not be reloaded"));
      return;
    }
    const retryableContention = payload?.code === "session_operation_pending";
    const errorMessage = payload?.error || (retryableContention ? "Another session operation is pending. Please retry." : "Prompt failed to send");
    showPromptFailure(errorMessage, { retryableContention });
  } else if (response.ok) {
    const payload = responsePayload;
    if (stopHandlingChangedSubmittedView()) return;
    if (submittedPromptSuperseded()) return;
    if (cloneCommand && payload?.cancelled) {
      restoreSubmittedPromptInput();
      setComposerState("idle");
      showStatus("Clone cancelled", true);
      return;
    }
    if (payload?.command === "name") {
      if (payload.error) {
        restoreSubmittedPromptInput();
        if (queuedPrompt) showCurrentActiveTask("error", payload.error);
        else setComposerState("error", payload.error);
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
      if (queuedPrompt) showCurrentActiveTask();
      else setComposerState("done", payload.current ? "Named" : "Name set");
      showStatus(payload.current ? `Session name: “${payload.name}”` : eventStatusText({ type: "session_info", name: payload.name }), true);
      appendSessionNameFeedback(payload);
      sidebarController.refresh().catch(() => {});
      return;
    }
    clearStoredComposerDraft(submittedSession);
    if (payload?.command === "reload") {
      setComposerState("success", "Reloaded");
      refreshCommandsAfterReload();
      showStatus("Pi resources reloaded", true);
      return;
    }
    if (payload?.command === "compact") {
      sidebarController.refresh().catch(() => {});
      if (composerState?.dataset.state === "sending") setComposerState("running", "Compacting…");
      showStatus("Compaction started", true);
      return;
    }
    if (payload?.command === "fork") {
      if (queuedPrompt) showCurrentActiveTask();
      else setComposerState("idle", "", { focus: false });
      showStatus("Choose a fork point", true);
      openForkSessionModal();
      return;
    }
    if (payload?.command === "tree") {
      if (queuedPrompt) showCurrentActiveTask();
      else setComposerState("idle", "", { focus: false });
      showStatus("Choose a tree entry", true);
      openTreeSessionModal();
      return;
    }
    if (payload?.command === "model") {
      if (queuedPrompt) showCurrentActiveTask();
      else setComposerState("idle", "", { focus: false });
      openModelSettingsModal();
      return;
    }
    if (payload?.command === "login" || payload?.command === "logout") {
      showCurrentActiveTask("done", "Instructions shown");
      if (queuedPrompt && composerState?.dataset.state === "running") selectStreamingBehavior(streamingBehavior, { focus: false });
      liveMessageRenderer.appendMessage("gateway", payload.message, true, true, new Date(), { markdown: true });
      return;
    }
    if (payload?.session && promptSessionInput && payload.session !== promptSessionInput.value) {
      await switchSession(payload.redirect || `/?session=${encodeURIComponent(payload.session)}`, { push: true, focus: true });
      return;
    }
    if (cloneCommand || newCommand) hideSessionSwitching();
    if (payload?.queued_after_compaction || payload?.compacting) {
      liveAgentRunning = payload?.running === true;
      setComposerState("running", "Compacting…", { since: previousWaitingForOutputSince });
      if (payload?.queued_after_compaction) showStatus("Queued for after compaction", true);
    } else if (payload?.running === false) {
      setComposerState("done", "Done");
      showStatus("Done");
    } else {
      liveAgentRunning = true;
      setComposerState("running", WORKING_LABEL);
      if (payload?.follow_up) showStatus("Sent to follow-up queue", true);
      else if (payload?.steer) showStatus("Steered Pi", true);
    }
  } else {
    clearStoredComposerDraft(submittedSession);
    liveAgentRunning = true;
    setComposerState("running", WORKING_LABEL);
  }
  conversationController.scrollToBottom();
}

async function clearQueuedMessages(event) {
  const button = event.currentTarget;
  if (button.disabled || sessionSyncBlocked()) return;
  if (!window.confirm("Remove all queued messages? Pi will keep running.")) return;

  const generation = sessionViewGeneration;
  const queueGeneration = queueViewGeneration;
  const session = currentSessionPath();
  const current = () => generation === sessionViewGeneration && queueGeneration === queueViewGeneration && session === currentSessionPath();
  const errorMessage = document.querySelector("[data-clear-queue-error]");
  errorMessage.hidden = true;
  button.disabled = true;
  try {
    const response = await fetch("/clear_queue", {
      method: "POST",
      body: new URLSearchParams({ session }),
      headers: { "Accept": "application/json" }
    });
    const payload = await response.json().catch(() => ({}));
    if (!response.ok || !payload.ok) throw new Error(payload.error || "Could not clear the queue. Please try again.");
    if (!current()) return;
    // Do not advance lastEventSeq: tools and messages still need their poll replay.
    reconcileQueuedMessages(payload.queued_messages, payload.event_sequence);
  } catch (error) {
    if (!current()) return;
    errorMessage.textContent = error.message || "Could not clear the queue. Please try again.";
    errorMessage.hidden = false;
  } finally {
    if (current()) button.disabled = sessionSyncBlocked();
  }
}

async function submitAbort(event) {
  event.preventDefault();
  if (!abortForm || abortForm.dataset.submitting === "true") return;

  promptSubmissionGeneration += 1;
  if (composerState?.dataset.state !== "stopping") {
    setComposerState("stopping", "Stopping current task…");
    showStatus("Stopping current task…", true);
  }
  const submittedForm = abortForm;
  const submittedSession = currentSessionPath();
  submittedForm.dataset.submitting = "true";
  abortEventPoll();
  showSessionSwitching();
  try {
    const response = await fetch(submittedForm.action, { method: "POST", body: new FormData(submittedForm), headers: { "Accept": "application/json" } });
    if (!response.ok) throw new Error("Stop failed");
    const payload = await response.json();
    if (submittedSession === currentSessionPath() && payload.forced && payload.editorText !== undefined) {
      liveAgentRunning = false;
      conversationController.setActivityRunning(false);
      liveBusySince = null;
      if (liveOutput) liveOutput.dataset.composerCompacting = "false";
      liveMessageRenderer.clearActiveActivity();
      liveMessageRenderer.clearLiveAssistantStreaming();
      liveMessageRenderer.resetLiveAssistantTracking();
      liveMessageRenderer.renderQueuedMessages({ steering: [], followUp: [] });
      liveMessageRenderer.removePendingCompactionMessage();
      setComposerState("done", "Done");
      if (payload.editorText && promptTextarea) {
        promptTextarea.value = [payload.editorText, promptTextarea.value].filter((text) => text.trim()).join("\n\n");
        promptTextarea.dispatchEvent(new Event("input", { bubbles: true }));
      }
    }
  } catch (_error) {
    stoppingSessionPaths.delete(submittedSession);
    if (submittedSession === currentSessionPath() && composerState?.dataset.state === "stopping") {
      showCurrentActiveTask();
      showStatus("Stop failed", true);
    }
  } finally {
    delete submittedForm.dataset.submitting;
    hideSessionSwitching();
    scheduleNextEventPoll(0);
    sidebarController.refresh().catch(() => {});
  }
}

function confirmOrStopRunningTask(event) {
  if (!["running", "bash"].includes(composerState?.dataset.state)) return false;

  event.preventDefault();
  if (event.repeat) return true;

  const now = Date.now();
  if (now <= escapeStopConfirmationExpiresAt) {
    setComposerState("stopping", "Stopping current task…");
    showStatus("Stopping current task…", true);
    abortForm.requestSubmit();
    return true;
  }

  escapeStopConfirmationExpiresAt = now + ESCAPE_STOP_CONFIRMATION_WINDOW_MS;
  clearTimeout(escapeStopConfirmationTimer);
  escapeStopConfirmationTimer = setTimeout(updateWaitingForOutputStatus, ESCAPE_STOP_CONFIRMATION_WINDOW_MS + 1);
  updateWaitingForOutputStatus();
  showStatus("Press ESC again to stop current task", true);
  return true;
}

function visibleCommands() {
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
  commandList?.querySelectorAll(".command-list h3").forEach((heading) => { heading.hidden = false; });
  commandList?.querySelectorAll(".command").forEach((command) => {
    command.hidden = false;
    command.classList.remove("is-highlighted");
  });
}

function filterCommandsFromPrompt() {
  if (!commandList || !promptTextarea) return;
  commandList.querySelectorAll(".command-list h3").forEach((heading) => { heading.hidden = false; });
  const query = promptTextarea.value.startsWith("/") ? promptTextarea.value.slice(1).trim().toLowerCase() : "";
  commandList.querySelectorAll(".command").forEach((command) => {
    command.hidden = query && !command.dataset.commandName.toLowerCase().includes(query);
  });
  highlightedCommandIndex = 0;
  updateHighlightedCommand();
}

function selectCommand(command) {
  if (!command || !promptTextarea) return false;
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
  const generation = sessionSwitchGeneration.capture();
  if (!list || list.dataset.loaded === "true") return;
  const url = list.dataset.commandsUrl;
  if (!url || list.dataset.loading === "true") return;

  list.dataset.loading = "true";
  try {
    const response = await fetch(url);
    if (!response.ok || commandList !== list || !list.isConnected || !sessionSwitchGeneration.current(generation) || list.dataset.commandsUrl !== url) return;
    const html = await response.text();
    if (commandList !== list || !list.isConnected || !sessionSwitchGeneration.current(generation) || list.dataset.commandsUrl !== url) return;
    list.outerHTML = html;
    commandList = document.getElementById("command-list");
    if (commandList) commandList.dataset.commandsUrl = url;
    highlightedCommandIndex = 0;
    if (promptTextarea?.value.startsWith("/")) {
      commandList?.classList.add("is-visible");
      commandList?.setAttribute("open", "");
      filterCommandsFromPrompt();
    }
  } catch (_error) {
  } finally {
    const reloadAfterLoading = list.dataset.reloadAfterLoading === "true";
    delete list.dataset.loading;
    delete list.dataset.reloadAfterLoading;
    if (reloadAfterLoading && commandList && sessionSwitchGeneration.current(generation) && commandList.dataset.commandsUrl === url) {
      commandList.dataset.loaded = "false";
      ensureCommandsLoaded();
    }
  }
}

function refreshCommandsAfterReload() {
  if (!commandList) return;
  commandList.dataset.loaded = "false";
  if (commandList.dataset.loading === "true") {
    commandList.dataset.reloadAfterLoading = "true";
    return;
  }
  ensureCommandsLoaded();
}

function updateCommandListForPrompt() {
  if (!commandList || !promptTextarea) return;

  if (promptTextarea.value.startsWith("/")) {
    commandList.classList.add("is-visible");
    commandList.setAttribute("open", "");
    ensureCommandsLoaded();
    filterCommandsFromPrompt();
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
  document.addEventListener("keydown", blockSessionSwitchingKeyboard, true);
  document.addEventListener("keydown", recordKeyboardConversationScrollIntent);
}

function bindSessionControls() {
  promptTextarea?.addEventListener("keydown", (event) => {
    if (event.isComposing) return;

    if (promptTextarea.value.startsWith("/") && commandList?.classList.contains("is-visible")) {
      const commands = visibleCommands();
      if (event.key === "ArrowDown" && commands.length > 0) {
        event.preventDefault();
        moveHighlightedCommand(1);
        return;
      }
      if (event.key === "ArrowUp" && commands.length > 0) {
        event.preventDefault();
        moveHighlightedCommand(-1);
        return;
      }
      if (((event.key === "Enter" && !event.shiftKey) || (event.key === "Tab" && !event.shiftKey)) && commands.length > 0) {
        event.preventDefault();
        selectHighlightedCommand();
        if (event.key === "Enter" && automaticComposerFocusEnabled()) {
          keyboardStreamingBehaviorOverride = event.altKey ? "follow_up" : null;
          promptForm.requestSubmit();
        }
        return;
      }
    }

    if (composerAutocompleteController.handleKeydown(event)) return;

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
      keyboardStreamingBehaviorOverride = event.altKey ? "follow_up" : null;
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

  let touchSendMenuPointerDown = false;
  sendMenuToggle?.addEventListener("click", (event) => {
    const opening = sendMenu?.hidden === true;
    closeSendMenu();
    if (!opening || !sendMenu) return;
    sendMenu.hidden = false;
    sendMenuToggle.setAttribute("aria-expanded", "true");
    if (event.detail === 0) sendMenu.querySelector('[data-streaming-behavior][aria-pressed="true"]')?.focus();
  });
  sendControl?.addEventListener("focusout", (event) => {
    if (touchSendMenuPointerDown) return;
    if (!sendControl.contains(event.relatedTarget)) closeSendMenu(null);
  });
  sendControl?.addEventListener("keydown", () => {
    touchSendMenuPointerDown = false;
  });
  sendMenu?.querySelectorAll("[data-streaming-behavior]").forEach((button) => {
    button.addEventListener("pointerdown", (event) => {
      touchSendMenuPointerDown = event.pointerType === "touch";
    });
    button.addEventListener("pointercancel", () => {
      touchSendMenuPointerDown = false;
    });
    button.addEventListener("click", () => {
      selectStreamingBehavior(button.dataset.streamingBehavior);
      touchSendMenuPointerDown = false;
    });
  });
  promptTextarea?.addEventListener("input", () => {
    resizePromptTextarea();
    persistStoredComposerDraft();
    if (!commandList) return;

    updateCommandListForPrompt();
  });

  if (!extensionUiControlsBound) {
    extensionUiControlsBound = true;
    extensionUiForm?.addEventListener("submit", submitExtensionUiDialog);
    // Like the composer, Enter submits only with a hardware keyboard; touch keyboards need it for new lines.
    extensionUiEditor?.addEventListener("keydown", (event) => {
      if (event.key !== "Enter" || event.shiftKey || event.isComposing || !automaticComposerFocusEnabled()) return;
      event.preventDefault();
      extensionUiForm.requestSubmit();
    });
    extensionUiModal?.querySelectorAll("[data-extension-ui-cancel]").forEach((button) => button.addEventListener("click", cancelExtensionUiRequest));
  }

  promptForm?.addEventListener("submit", submitPrompt);
  abortForm?.addEventListener("submit", submitAbort);
  document.querySelector("[data-clear-queue]")?.addEventListener("click", clearQueuedMessages);
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
  pendingFinalAssistantReply = null;
  currentSessionFindController.close({ restoreFocus: false });
  imageViewerController.close();
  liveMessageRenderer.releaseMessageImageObjectURLs(conversationPanel);
  composerAutocompleteController.destroy();
  clearTimeout(extensionUiTimeoutTimer);
  extensionUiTimeoutTimer = null;
  extensionUiRequestQueue = [];
  activeExtensionUiRequest = null;
  closeModal(extensionUiModal);
  resetExtensionUiModal();
  projectSelectController.destroy(conversationPanel);
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
  liveBash = null;
  liveBusySince = null;
  liveErrorText = "";
  resetEventPollBackoff();
  clearTimeout(escapeStopConfirmationTimer);
  escapeStopConfirmationTimer = null;
  escapeStopConfirmationExpiresAt = 0;
  stopWaitingForOutput();
  lastEventSeq = 0;
  lastQueueSeq = 0;
  hideReconnectBanner();
  clearAttachments();
  const modelModal = document.querySelector('[data-modal="model-settings-modal"]');
  if (modelModal) modelModal.hidden = true;
  modelSettingsOperationGeneration += 1;
  document.body.classList.toggle("modal-open", piModalIsOpen());
}

function replaceNewSessionModalHtml(html) {
  const currentModal = document.querySelector('[data-modal="new-session-modal"]');
  if (!html || !currentModal) return;

  newSessionFormController.destroy(currentModal);
  currentModal.outerHTML = html;
  newSessionFormController.initialize(document.querySelector('[data-modal="new-session-modal"]'));
}

function replaceForkSessionModalHtml(html) {
  if (!html) return;

  const template = document.createElement("template");
  template.innerHTML = html;
  ["fork-session-modal", "tree-session-modal"].forEach((name) => {
    const currentModal = document.querySelector(`[data-modal="${name}"]`);
    const replacement = template.content.querySelector(`[data-modal="${name}"]`);
    if (currentModal && replacement) currentModal.replaceWith(replacement.cloneNode(true));
  });
}

function showSessionSwitching() {
  conversationController.dismissQuoteSelection();
  document.body.classList.add("session-switching");
}

function hideSessionSwitching() {
  if (sessionNavigationPending) return;

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

function readMainSessionHistory() {
  try {
    const history = JSON.parse(window.sessionStorage.getItem(MAIN_SESSION_HISTORY_KEY) || "{}");
    return {
      current: typeof history.current === "string" ? history.current : "",
      previous: typeof history.previous === "string" ? history.previous : ""
    };
  } catch (_error) {
    return { current: "", previous: "" };
  }
}

function rememberMainSessionSelection(sessionPath) {
  if (!sessionPath || new URLSearchParams(window.location.search).get("session_only") === "1") return;

  const history = readMainSessionHistory();
  if (history.current === sessionPath) return;
  try {
    window.sessionStorage.setItem(MAIN_SESSION_HISTORY_KEY, JSON.stringify({ current: sessionPath, previous: history.current }));
  } catch (_error) {
  }
}

function detachedSessionFallbackUrl(detachedSessionPaths) {
  const url = new URL("/", window.location.origin);
  const previousSessionPath = readMainSessionHistory().previous;
  if (previousSessionPath && !detachedSessionPaths.includes(previousSessionPath)) url.searchParams.set("session", previousSessionPath);
  for (const path of detachedSessionPaths) url.searchParams.append("session_fallback_excluding", path);
  return `${url.pathname}${url.search}`;
}

function detachSession(paths = [currentSessionPath()]) {
  return switchSession(detachedSessionFallbackUrl(paths), { push: true, focus: true });
}

async function switchSession(url, { push = true, focus = true, preserveScroll = false, findQuery = null, fallbackNavigation = true } = {}) {
  const scrollSnapshot = preserveScroll ? conversationScrollSnapshot() : null;
  persistStoredComposerDraft();
  sidebarController.invalidate({ clearSessionsLimit: true });
  sessionSwitchAbortController?.abort();
  const abortController = new AbortController();
  sessionSwitchAbortController = abortController;
  const switchGeneration = sessionSwitchGeneration.next();
  const refreshRequestVersion = sidebarController.refreshRequestVersion;
  const timeout = setTimeout(() => abortController.abort(), SESSION_SWITCH_TIMEOUT_MS);
  showSessionSwitching();
  clearTimeout(eventPollTimer);
  eventPollTimer = null;
  abortEventPoll();
  try {
    const response = await fetch(sessionFragmentUrl(url), { headers: { "Accept": "application/json" }, signal: abortController.signal });
    if (!sessionSwitchGeneration.current(switchGeneration)) return false;
    if (!response.ok) throw new Error("Session fragment failed");

    const payload = await response.json();
    if (!sessionSwitchGeneration.current(switchGeneration)) return false;
    resetSessionViewState();
    sidebarController.replace(payload.sidebar_html, { notify: false });
    conversationPanel.outerHTML = payload.conversation_html;
    replaceNewSessionModalHtml(payload.new_session_modal_html);
    replaceForkSessionModalHtml(payload.fork_session_modal_html);
    bindSessionDom();
    bindSessionControls();
    rememberMainSessionSelection(payload.session);
    if (push) history.pushState({ session: payload.session }, payload.title || "", payload.url || url);
    if (typeof baseDocumentTitle !== "undefined") {
      baseDocumentTitle = payload.title ? `${payload.title} · Gripi` : "Gripi";
      extensionDocumentTitle = null;
      renderDocumentTitle();
    } else {
      document.title = payload.title ? `${payload.title} · Gripi` : "Gripi";
    }
    sidebarController.closeMobile();
    initializeSessionView({ focus, scrollSnapshot, findQuery });
    if (refreshRequestVersion !== sidebarController.refreshRequestVersion) sidebarController.scheduleRefresh(0);
    return true;
  } catch (_error) {
    if (!sessionSwitchGeneration.current(switchGeneration)) return false;
    // A gateway that is restarting would answer the navigation with its proxy's error page.
    if (fallbackNavigation && !gatewayUpdateController.overlay.open) {
      sessionNavigationPending = true;
      window.location.href = url;
    } else {
      showReconnectBanner();
      sidebarController.scheduleRefresh(0);
    }
    return false;
  } finally {
    clearTimeout(timeout);
    if (sessionSwitchAbortController === abortController) sessionSwitchAbortController = null;
    if (sessionSwitchGeneration.current(switchGeneration)) hideSessionSwitching();
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

async function markCurrentSessionRead(readState = null) {
  const pending = readState || {
    sessionPath: currentSessionPath(),
    responseCount: liveOutput?.dataset.assistantResponseCount,
    sessionGeneration: liveOutput?.dataset.sessionGeneration
  };
  if (!pending.sessionPath || (!readState && sidebarController.element)) return;
  if (!readState && (document.hidden || !document.hasFocus())) {
    markReadAfterVisible = pending.sessionPath;
    return;
  }
  if (!readState) markReadAfterVisible = null;
  if (markReadInFlight) {
    markReadQueued.set(pending.sessionPath, pending);
    return;
  }

  markReadInFlight = true;
  const body = new URLSearchParams({
    session: pending.sessionPath,
    assistant_response_count: pending.responseCount,
    session_generation: pending.sessionGeneration
  });
  try {
    await fetch("/sessions/mark_read", { method: "POST", body });
  } catch (_error) {
  } finally {
    markReadInFlight = false;
    const next = markReadQueued.entries().next().value;
    if (next) {
      markReadQueued.delete(next[0]);
      markCurrentSessionRead(next[1]);
    }
  }
}

function markCurrentSessionReadAfterVisible() {
  const sessionPath = markReadAfterVisible;
  markReadAfterVisible = null;
  if (sessionPath === currentSessionPath()) markCurrentSessionRead();
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
  if (sessionSwitching()) return;

  const modal = document.querySelector('[data-modal="new-session-modal"]');
  openModal(modal);
  newSessionFormController.open(modal?.querySelector(".new-session-cwd-form"));
}

function piModalIsOpen() {
  return !!document.querySelector("[data-modal]:not([hidden])");
}

function modalIsOpen() {
  return imageViewerController.isOpen || piModalIsOpen();
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
  if (!modal || modal.dataset.sessionActionPending === "true") return;
  if (modal.dataset.modal === "new-session-modal") newSessionFormController.cancelBrowse(modal.querySelector(".new-session-cwd-form"));
  if (modal.dataset.modal === "environment-modal") environmentController.closeForm();
  modal.hidden = true;
  if (modal.dataset.modal === "model-settings-modal") modelSettingsOperationGeneration += 1;
  document.body.classList.toggle("modal-open", piModalIsOpen());
  if (!piModalIsOpen() && !document.hidden) {
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
  } else if (["session-rename-modal", "session-delete-modal"].includes(modal?.dataset.modal)) {
    if (!sessionActionsController.restoreFocus()) syncComposerFocus();
  } else if (["new-session-modal", "command-palette-modal", "environment-modal"].includes(modal?.dataset.modal)) {
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
  list.removeAttribute("role");
  const status = document.createElement("p");
  status.className = "picker-status";
  status.dataset.forkSessionStatus = "";
  status.textContent = text;
  list.append(status);
}

function handlePickerListKey(event) {
  if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
  // offsetParent is null while a list or one of its ancestors is hidden.
  const list = [...document.querySelectorAll("[data-modal]:not([hidden]) [data-picker-list]")].find((candidate) => candidate.offsetParent);
  if (!list) return;
  event.preventDefault();
  const current = [...list.querySelectorAll('[role="option"]')].findIndex((row) => row.getAttribute("aria-selected") === "true");
  movePickerCursor(list, current + (event.key === "ArrowDown" ? 1 : -1));
}

async function loadForkMessages(modal) {
  const list = modal?.querySelector("[data-fork-session-list]");
  const url = list?.dataset.forkMessagesUrl;
  if (!list || !url || list.dataset.loading === "true") return;

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
    list.setAttribute("role", "listbox");
    messages.forEach((message, index) => {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "picker-row fork-session-option";
      button.setAttribute("role", "option");
      button.dataset.forkEntryId = message.entryId || message.entry_id || "";
      const cursor = document.createElement("span");
      cursor.className = "picker-cursor";
      cursor.setAttribute("aria-hidden", "true");
      cursor.textContent = "›";
      const text = document.createElement("span");
      text.className = "fork-session-text";
      text.textContent = message.text || "Untitled prompt";
      const meta = document.createElement("span");
      meta.className = "fork-session-meta";
      meta.textContent = `Message ${index + 1} of ${messages.length}`;
      button.append(cursor, text, meta);
      list.append(button);
    });
    // Like Pi CLI, the cursor starts on the most recent message.
    movePickerCursor(list, messages.length - 1);
  } catch (_error) {
    setForkSessionStatus(modal, "Could not load fork points.");
  } finally {
    delete list.dataset.loading;
  }
}

function addSessionViewFormParams(formData) {
  const project = new URLSearchParams(window.location.search).get("project");
  if (project) formData.set("project", project);
  const sessionSearch = sidebarController.activeSearch();
  if (sessionSearch) formData.set("session_search", sessionSearch);
  const tag = new URLSearchParams(window.location.search).get("tag");
  if (tag) formData.set("tag", tag);
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
  treeSessionController.open();
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
    if (opener.dataset.modalOpen === "environment-modal") {
      environmentController.open();
      return;
    }
    openModal(modal);
    return;
  }

  const forkOption = event.target.closest("[data-fork-entry-id]");
  if (forkOption) {
    event.preventDefault();
    const modal = forkOption.closest("[data-modal]");
    const forkMeta = forkOption.querySelector(".fork-session-meta");
    const originalForkMeta = forkMeta.textContent;
    const formData = new FormData();
    formData.set("session", currentSessionPath());
    formData.set("entry_id", forkOption.dataset.forkEntryId);
    addSessionViewFormParams(formData);
    forkOption.disabled = true;
    forkMeta.textContent = "Forking…";
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
        forkMeta.textContent = originalForkMeta;
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
    clearTimeout(eventPollTimer);
    eventPollTimer = null;
    abortEventPoll();
    const takeoverSession = currentSessionPath();
    const takeoverGeneration = sessionSwitchGeneration.capture();
    const takeoverCurrent = () => takeoverSession === currentSessionPath() && sessionSwitchGeneration.current(takeoverGeneration);
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
        if (!takeoverCurrent()) return;
        if (!response.ok || !payload?.ok) throw new Error(payload?.error || "Could not take over session");
        await refreshCurrentSessionPreservingComposer();
        scheduleNextEventPoll(0);
      })
      .catch((error) => {
        if (!takeoverCurrent()) return;
        scheduleNextEventPoll(0);
        takeoverButton.disabled = false;
        takeoverButton.textContent = originalText;
        if (errorOutput) {
          errorOutput.textContent = error.message;
          errorOutput.hidden = false;
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

});

document.addEventListener("input", (event) => {
  if (event.target.closest("[data-model-search]") && !modelSettingsPending) renderModelSettingsModels();
});

document.addEventListener("click", (event) => {
  const scope = event.target.closest("[data-model-scope-option]");
  if (!scope) return;
  setModelSettingsScope(scope.dataset.modelScopeOption);
  if (automaticComposerFocusEnabled()) document.querySelector("[data-model-search]")?.focus();
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
  if (form.dataset.submitting === "true") return;

  const formData = new FormData(form);
  addSessionViewFormParams(formData);
  const modal = form.closest("[data-modal]");
  form.dataset.submitting = "true";
  newSessionFormController.setStatus(form, "Starting…");
  showSessionSwitching();
  try {
    const response = await fetch(form.action, { method: "POST", body: formData, headers: { "Accept": "application/json" } });
    if (!response.ok) {
      const payload = await response.json().catch(() => null);
      newSessionFormController.setStatus(form, payload?.error || "Path must be an existing directory.", true);
      return;
    }
    const payload = await response.json();
    closeModal(modal);
    await switchSession(payload.redirect || `/?session=${encodeURIComponent(payload.session)}`, { push: true, focus: true });
  } catch (_error) {
    newSessionFormController.setStatus(form, "Could not start the session. Try again.", true);
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

function handleModalTab(event) {
  if (event.key !== "Tab" || event.defaultPrevented) return;
  const modal = document.querySelector('dialog[open]') || document.querySelector('[data-modal]:not([hidden])');
  if (!modal) return;
  const focusable = [...modal.querySelectorAll('button:not(:disabled), input:not(:disabled):not([type="hidden"]), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])')]
    .filter((element) => element.tabIndex >= 0 && !element.closest("[hidden]"));
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
    if (openModalElement === extensionUiModal && activeExtensionUiRequest) {
      cancelExtensionUiRequest();
      return;
    }
    closeModal(openModalElement);
    return;
  }

  // A held key toggles once, and its repeats stay away from the browser's own Ctrl+K.
  if (isCtrlOrMetaShortcut(event, "k") && !event.shiftKey && (event.repeat || commandPaletteController.toggle())) {
    event.preventDefault();
    return;
  }
  handleModelSettingsKey(event);
  handlePickerListKey(event);
  handleModalTab(event);
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

  if (event.key === "Escape" && sendMenu && !sendMenu.hidden) {
    event.preventDefault();
    closeSendMenu(sendMenuToggle);
    return;
  }

  if (event.key === "Escape" && !event.defaultPrevented && confirmOrStopRunningTask(event)) return;

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
  if (!event.target.closest(".send-control")) closeSendMenu();
  if (!event.target.closest(".session-sidebar")) exitSessionShortcutMode();
});

document.addEventListener("gripi:sidebar-filtered", (event) => {
  replaceNewSessionModalHtml(event.detail.modalHtml);
});

document.addEventListener("gripi:sidebar-selected-title", (event) => {
  updateSessionHeaderName(event.detail.title);
});

document.addEventListener("click", (event) => {
  const link = event.target.closest(".session-header-window-action");
  if (!link || !normalLeftClick(event)) return;

  detachSession().catch(() => {});
});

document.addEventListener("click", async (event) => {
  const link = event.target.closest(".session-sidebar a.session");
  exitSessionShortcutMode();
  if (!link || !normalLeftClick(event)) return;

  event.preventDefault();
  const findQuery = link.dataset.sessionFindQuery || null;
  if (link.classList.contains("selected")) {
    sidebarController.closeMobile();
    if (findQuery) currentSessionFindController.show(findQuery).catch(() => {});
    return;
  }

  await switchSession(link.href, { push: true, focus: true, findQuery });
});

document.addEventListener("submit", async (event) => {
  const form = event.target.closest('form[action="/sessions/new"]');
  if (!form) return;

  event.preventDefault();
  const switchGeneration = sessionSwitchGeneration.capture();
  const viewGeneration = sessionViewGeneration;
  let navigatingAway = false;
  showSessionSwitching();
  try {
    const formData = new FormData(form);
    addSessionViewFormParams(formData);
    const response = await fetch(form.action, { method: "POST", body: formData, headers: { "Accept": "application/json" } });
    if (!sessionSwitchGeneration.current(switchGeneration) || viewGeneration !== sessionViewGeneration) return;
    if (!response.ok) {
      navigatingAway = true;
      form.submit();
      return;
    }

    const payload = await response.json();
    if (!sessionSwitchGeneration.current(switchGeneration) || viewGeneration !== sessionViewGeneration) return;
    await switchSession(payload.redirect || `/?session=${encodeURIComponent(payload.session)}`, { push: true, focus: true });
  } finally {
    if (!navigatingAway && sessionSwitchGeneration.current(switchGeneration) && viewGeneration === sessionViewGeneration) hideSessionSwitching();
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

function restoreSessionLiveState({ resetIdleState = false } = {}) {
  liveMessageRenderer.clearActiveActivity();
  const initialComposerState = liveOutput.dataset.composerState;
  const initialComposerStateSince = Number(liveOutput.dataset.composerStateSince || 0);
  const initialComposerCompacting = liveOutput.dataset.composerCompacting === "true";
  liveBusySince = Number(liveOutput.dataset.composerBusySince || 0) || null;
  const initialComposerLabel = initialComposerCompacting ? "Compacting…" : WORKING_LABEL;
  liveAgentRunning = liveOutput.dataset.agentRunning === "true";
  conversationController.setActivityRunning(liveAgentRunning);
  liveMessageRenderer.restorePersistedBashExecutions();
  liveMessageRenderer.restoreCompletedBashExecutions();
  const activeBashEvent = liveMessageRenderer.restoreActiveBash();
  liveBash = activeBashEvent ? {
    id: activeBashEvent.bashId,
    command: activeBashEvent.command,
    excludeFromContext: activeBashEvent.excludeFromContext,
    startedAt: eventTimeMilliseconds(activeBashEvent)
  } : null;
  if (["running", "bash"].includes(initialComposerState)) {
    if (stoppingSessionPaths.has(currentSessionPath())) setComposerState("stopping", "Stopping current task…", { focus: false });
    else if (initialComposerState === "bash") setComposerState("bash", "Shell command running…", { focus: false });
    else setComposerState(initialComposerState, initialComposerLabel, { since: initialComposerStateSince, focus: false });
  } else {
    stoppingSessionPaths.delete(currentSessionPath());
    if (resetIdleState) setComposerState(initialComposerState || "idle", "", { focus: false });
  }
  if (initialComposerCompacting) liveMessageRenderer.appendPendingCompactionMessage(new Date(initialComposerStateSince || Date.now()));
  const activeAssistantEvent = liveOutput.dataset.activeAssistantEvent;
  delete liveOutput.dataset.activeAssistantEvent;
  try {
    const event = JSON.parse(activeAssistantEvent || "null");
    if (event) renderEvent(event);
  } catch (_error) {
  }
  liveMessageRenderer.restoreActiveToolExecutions();
  hydrateExtensionUiState();
}

function initializeSessionView({ focus = true, scrollSnapshot = null, findQuery = null } = {}) {
  const generation = sessionViewGeneration;
  notificationPresenceController.sessionChanged();
  newSessionFormController.initialize();
  ensureNotificationWorker().catch(() => {});
  browserAccessController.resume();
  workspaceAccessController.resume();
  if (liveOutput) {
    lastSessionSyncAt = Date.now();
    enhanceMarkdownCodeBlocks(conversationScroll);
    enhanceMessageLinks(conversationScroll);
    resetEventCursor();
    refreshSessionStatus(generation).catch(() => {});
    restoreSessionLiveState();
    scheduleNextEventPoll(0);
    if (!scrollSnapshot || scrollSnapshot.nearBottom) conversationController.positionInitialAtBottom();
    const focusedElement = document.activeElement;
    requestAnimationFrame(async () => {
      await liveMessageRenderer.terminalHydration;
      if (generation !== sessionViewGeneration) return;
      loadStoredComposerDraft();
      updatePromptPlaceholder();
      resizePromptTextarea();
      const focusUnchanged = document.activeElement === focusedElement;
      if (focus && focusUnchanged) syncComposerFocus();
      if (!restorePreservedConversationScroll(scrollSnapshot)) conversationController.forceInitialBottomFollow();
      if (findQuery && focusUnchanged) currentSessionFindController.show(findQuery).catch(() => {});
    });
  }
  sidebarController.scheduleRefresh();
  gatewayUpdateController.check({ refresh: true }).catch(() => {});
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
  if (markReadAfterVisible) markCurrentSessionReadAfterVisible();
  sidebarController.scheduleRefresh();
});
window.addEventListener("pageshow", () => resumeEventPolling().catch(() => {}));
window.addEventListener("focus", () => {
  if (markReadAfterVisible) markCurrentSessionReadAfterVisible();
  resumeEventPolling().catch(() => {});
});
window.addEventListener("online", () => resumeEventPolling().catch(() => {}));
window.addEventListener("popstate", () => switchSession(window.location.href, { push: false, focus: true }));

function bootstrapPage() {
  gatewayUpdateController.cleanNavigation();
  webPushController.prepare().catch(() => {});
  webPushController.reconcile().then((enabled) => {
    webPushEnabled = enabled;
    updateNotificationToggle();
  }).catch(() => {});
  sidebarController.initialize();
  sessionActionsController.initialize();
  sessionTagsController.initialize();
  bindPageLifetimeControls();
  bindSessionDom();
  bindSessionControls();
  rememberMainSessionSelection(currentSessionPath());
  notificationPresenceController.start();
  initializeSessionView();
  gatewayUpdateController.resume();
  resourceUsageController.start();
}

bootstrapPage();
