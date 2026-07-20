import { enhanceMarkdownCodeBlocks } from "./dom.js";

const FOCUSED_ACTIVITY_ITEM_LIMIT = 10;

export class ConversationController {
  constructor(document, window) {
    this.document = document;
    this.window = window;
    this.bindingEpoch = 0;
    this.element = null;
    this.liveOutput = null;
    this.listeners = [];
    this.timers = new Set();
    this.frames = new Set();
    this.historyAbortController = null;
    this.historyIntersectionObserver = null;
    this.historyRequestGeneration = 0;
    this.olderWindowPromise = null;
    this.historyWindowAfterCursor = null;
    this.olderHistoryPromise = null;
    this.autoScrollEnabled = true;
    this.forceBottomAutoScroll = false;
    this.followOversizedMessageBottom = false;
    this.programmaticScroll = false;
    this.lastScrollTop = 0;
    this.scrollDirection = null;
    this.scrollIntent = null;
    this.lastRevealAt = 0;
    this.revealTimer = null;
    this.revealDelayTimer = null;
    this.messageJumpTargetsSuppressed = false;
    this.messageJumpSuppressionTimer = null;
    this.messageJumpSuppressionScrollEndListener = null;
    this.messageJumpSuppressionGeneration = 0;
    this.focusedView = false;
    this.agentRunning = false;
    this.focusedActivityRefreshFrame = null;
    this.focusedActivityMessageIds = new WeakMap();
    this.focusedActivityMessageSequence = 0;
    this.focusedActivitySignature = null;
  }

  bind(promptTextarea = null) {
    this.detach();
    this.bindingEpoch += 1;
    this.element = this.document.getElementById("conversation-scroll");
    this.liveOutput = this.document.getElementById("live-output");
    this.agentRunning = this.liveOutput?.dataset.agentRunning === "true";
    this.promptTextarea = promptTextarea;
    this.topJumpControls = this.document.querySelector(".jump-controls--top");
    this.bottomJumpControls = this.document.querySelector(".jump-controls--bottom");
    this.jumpToFirstButton = this.document.querySelector(".jump-to-first");
    this.jumpToLatestButton = this.document.querySelector(".jump-to-latest");
    this.conversationPanel = this.document.querySelector(".conversation-panel");
    this.viewSelect = this.document.querySelector("[data-conversation-view-select]");
    this.viewSelectControl = this.viewSelect?.closest?.("[data-project-select]")?._projectSelectState?.trigger || this.viewSelect;
    this.applyFocusedView();
    this.listen(this.viewSelect, "change", () => {
      this.focusedView = this.viewSelect.value === "conversation";
      this.applyFocusedView(true);
    });
    this.lastScrollTop = this.element?.scrollTop || 0;
    this.scrollDirection = null;
    this.followOversizedMessageBottom = false;
    if (!this.element) return;

    this.listen(this.element, "click", (event) => {
      const toggle = event.target.closest?.("[data-focus-activity-toggle]");
      if (toggle) this.toggleFocusedActivity(toggle);
    });
    this.refreshFocusedActivity();

    this.listen(this.element, "keydown", (event) => {
      if (event.key === "Home" && this.jumpToFirstButton && !event.altKey && !event.ctrlKey && !event.metaKey && !event.shiftKey && !event.target.closest?.("input, textarea, select, [contenteditable]")) {
        event.preventDefault();
        this.jumpToConversationTop();
        return;
      }
      if (event.key !== "Tab" || !this.promptTextarea) return;
      event.preventDefault();
      this.promptTextarea.focus({ preventScroll: true });
    });
    ["wheel", "touchstart", "pointerdown"].forEach((type) => {
      this.listen(this.element, type, () => this.recordScrollIntent("pointer"), { passive: true });
    });
    this.listen(this.element, "scroll", () => this.handleScroll(), { passive: true });
    this.listen(this.historyStatus(), "click", () => this.loadOlderWindow().catch(() => {}));
    this.observeHistoryStatus();
    this.listen(this.jumpToFirstButton, "click", () => {
      if (this.jumpToFirstButton.dataset.jumpTarget === "message") return this.scrollToMessageTop();

      this.jumpToConversationTop();
    });
    this.listen(this.jumpToLatestButton, "click", () => {
      if (this.jumpToLatestButton.dataset.jumpTarget === "message") this.scrollToMessageBottom();
      else this.scrollToBottom("auto", { force: true });
    });
  }

  detach() {
    this.listeners.forEach(([target, type, listener, options]) => target.removeEventListener?.(type, listener, options));
    this.listeners = [];
    this.timers.forEach((timer) => clearTimeout(timer));
    this.timers.clear();
    this.frames.forEach((frame) => cancelAnimationFrame(frame));
    this.frames.clear();
    this.historyAbortController?.abort();
    this.historyAbortController = null;
    this.historyIntersectionObserver?.disconnect();
    this.historyIntersectionObserver = null;
    this.olderWindowPromise = null;
    this.historyWindowAfterCursor = null;
    this.olderHistoryPromise = null;
    this.revealTimer = null;
    this.revealDelayTimer = null;
    this.messageJumpSuppressionTimer = null;
    if (this.focusedActivityRefreshFrame) cancelAnimationFrame(this.focusedActivityRefreshFrame);
    this.focusedActivityRefreshFrame = null;
    this.focusedActivitySignature = null;
    this.clearMessageJumpSuppressionScrollEndListener();
    this.messageJumpSuppressionGeneration += 1;
    this.messageJumpTargetsSuppressed = false;
    this.programmaticScroll = false;
    this.document.body.classList.remove("is-conversation-scrolling");
  }

  reset() {
    this.detach();
    this.bindingEpoch += 1;
    this.element = null;
    this.liveOutput = null;
    this.conversationPanel = null;
    this.viewSelect = null;
    this.viewSelectControl = null;
    this.agentRunning = false;
    this.autoScrollEnabled = true;
    this.forceBottomAutoScroll = false;
  }

  applyFocusedView(preserveScroll = false) {
    const scrollRect = preserveScroll ? this.element?.getBoundingClientRect?.() : null;
    const anchor = scrollRect && [...this.element.querySelectorAll(".message")]
      .filter((message) => this.focusedViewMessage(message))
      .find((message) => message.getBoundingClientRect().bottom > scrollRect.top);
    const scrollSnapshot = scrollRect ? {
      top: this.element.scrollTop,
      nearBottom: this.nearBottom(),
      anchor,
      anchorOffset: anchor ? anchor.getBoundingClientRect().top - scrollRect.top : null
    } : null;

    this.conversationPanel?.classList.toggle("is-conversation-focused", this.focusedView);
    if (this.viewSelect) {
      const value = this.focusedView ? "conversation" : "full";
      if (this.viewSelect.value !== value) {
        this.viewSelect.value = value;
        this.viewSelect.dispatchEvent?.(new this.window.Event("change", { bubbles: true }));
      }
    }

    if (!scrollSnapshot) return;
    if (scrollSnapshot.nearBottom) {
      this.element.scrollTop = this.element.scrollHeight;
    } else if (scrollSnapshot.anchor) {
      this.element.scrollTop += scrollSnapshot.anchor.getBoundingClientRect().top - scrollRect.top - scrollSnapshot.anchorOffset;
    } else {
      this.element.scrollTop = Math.min(scrollSnapshot.top, Math.max(0, this.element.scrollHeight - this.element.clientHeight));
    }
    this.autoScrollEnabled = scrollSnapshot.nearBottom;
    this.forceBottomAutoScroll = false;
    this.followOversizedMessageBottom = scrollSnapshot.nearBottom;
    this.lastScrollTop = this.element.scrollTop;
    this.updateJumpControls();
  }

  setAgentRunning(running) {
    if (this.agentRunning === running) return;
    this.agentRunning = running;
    this.scheduleFocusedActivityRefresh();
  }

  focusedViewMessage(message) {
    if (message.classList.contains("message--compaction")) return true;
    if (["message--thinking", "message--tool", "message--tool-call", "message--tool-transcript", "message--error", "message--tool-error"].some((name) => message.classList.contains(name))) return false;
    return !["system", "status", "tool", "toolResult", "error"].includes(message.dataset.role);
  }

  focusedActivityGroups(messages) {
    const groups = [];
    let group = [];
    let previousMessage = null;
    messages.forEach((message) => {
      if (previousMessage && this.historyGapBetween(previousMessage, message) && group.length > 0) {
        groups.push(group);
        group = [];
      }
      if (this.focusedViewMessage(message)) {
        if (group.length > 0) groups.push(group);
        group = [];
      } else {
        group.push(message);
      }
      previousMessage = message;
    });
    if (group.length > 0) groups.push(group);
    return groups;
  }

  historyGapBetween(first, second) {
    const gap = this.historyStatus();
    if (!gap || gap.hidden) return false;
    let sibling = first.nextElementSibling;
    while (sibling && sibling !== second) {
      if (sibling === gap) return true;
      sibling = sibling.nextElementSibling;
    }
    return false;
  }

  focusedActivitySummary(messages) {
    const reasoningCount = messages.filter((message) => message.classList.contains("message--thinking")).length;
    const toolCount = messages.filter((message) => ["message--tool", "message--tool-call", "message--tool-transcript"].some((name) => message.classList.contains(name))).length;
    const errorCount = messages.filter((message) => message.classList.contains("message--error") || message.classList.contains("message--tool-error")).length;
    const otherCount = messages.filter((message) => !message.classList.contains("message--thinking") && !["message--tool", "message--tool-call", "message--tool-transcript", "message--error"].some((name) => message.classList.contains(name)) && !message.classList.contains("message--tool-error")).length;
    const parts = [];
    if (reasoningCount > 0) parts.push(`${reasoningCount} reasoning ${reasoningCount === 1 ? "step" : "steps"}`);
    if (toolCount > 0) parts.push(`${toolCount} tool ${toolCount === 1 ? "update" : "updates"}`);
    if (otherCount > 0) parts.push(`${otherCount} other ${otherCount === 1 ? "update" : "updates"}`);
    return { text: parts.join(" · "), errorCount };
  }

  focusedActivityItems(messages) {
    const items = [];
    const toolCallIndexes = new Map();
    messages.forEach((message) => {
      const toolCall = message.classList.contains("message--tool-call");
      const error = message.classList.contains("message--error") || message.classList.contains("message--tool-error");
      if (!toolCall && !error) return;

      const source = message.querySelector(".compact-summary") || message.querySelector(".message-body");
      const text = source?.textContent?.replace(/\s+/g, " ").trim();
      if (!text && !error) return;
      const toolCallId = message.dataset.toolCallId;
      const existingIndex = toolCallId ? toolCallIndexes.get(toolCallId) : undefined;
      if (existingIndex !== undefined) {
        if (toolCall && text) items[existingIndex].text = text;
        if (error) items[existingIndex].type = "error";
        return;
      }

      if (toolCallId) toolCallIndexes.set(toolCallId, items.length);
      items.push({ type: error ? "error" : "tool", text: text || "Error" });
    });
    return items;
  }

  refreshFocusedActivity() {
    if (!this.element?.querySelectorAll) return;
    const messages = [...this.element.querySelectorAll(".message")];
    const signature = `${this.agentRunning}|${this.historyStatus()?.hidden !== false}|${messages.map((message) => {
      if (!this.focusedActivityMessageIds.has(message)) this.focusedActivityMessageIds.set(message, ++this.focusedActivityMessageSequence);
      return [
        this.focusedActivityMessageIds.get(message),
        this.focusedViewMessage(message),
        message.classList.contains("message--thinking"),
        ["message--tool", "message--tool-call", "message--tool-transcript"].some((name) => message.classList.contains(name)),
        message.classList.contains("message--error") || message.classList.contains("message--tool-error")
      ].join(":");
    }).join("|")}`;
    if (signature === this.focusedActivitySignature) return;
    this.focusedActivitySignature = signature;
    const summaries = [...this.element.querySelectorAll("[data-focus-activity-summary]")];
    const activeToggle = this.document.activeElement?.closest?.("[data-focus-activity-toggle]");
    const activeGroupId = activeToggle?.closest("[data-focus-activity-summary]")?.dataset.focusActivitySummary;
    const focusAnchor = activeGroupId && messages.find((message) => message.dataset.focusActivityGroup === activeGroupId);
    const expandedMessages = new Set();
    summaries.forEach((summary) => {
      if (summary.querySelector("[data-focus-activity-toggle]")?.getAttribute("aria-expanded") === "true") {
        messages.filter((message) => message.dataset.focusActivityGroup === summary.dataset.focusActivitySummary).forEach((message) => expandedMessages.add(message));
      }
      summary.remove();
    });
    messages.forEach((message) => { delete message.dataset.focusActivityGroup; });
    let replacementFocus = null;
    const groups = this.focusedActivityGroups(messages);
    groups.forEach((group, index) => {
      const groupId = `${this.bindingEpoch}-${index}`;
      const expanded = group.some((message) => expandedMessages.has(message));
      group.forEach((message) => { message.dataset.focusActivityGroup = groupId; });
      const summaryData = this.focusedActivitySummary(group);
      const items = this.focusedActivityItems(group);
      const running = this.agentRunning && index === groups.length - 1 && group.at(-1) === messages.at(-1);
      const summary = this.document.createElement("section");
      summary.className = `focus-activity-summary${summaryData.errorCount > 0 ? " has-errors" : ""}${expanded ? " is-expanded" : ""}${running ? " is-running" : ""}`;
      summary.dataset.focusActivitySummary = groupId;

      const header = this.document.createElement(items.length > 0 ? "button" : "div");
      header.className = "focus-activity-header";
      if (items.length > 0) {
        header.type = "button";
        header.dataset.focusActivityToggle = "true";
        header.setAttribute("aria-expanded", String(expanded));
      }
      if (running) {
        const spinner = this.document.createElement("span");
        spinner.className = "focus-activity-spinner";
        spinner.setAttribute("aria-hidden", "true");
        header.append(spinner);
      }
      if (summaryData.text) {
        const text = this.document.createElement("span");
        text.className = "focus-activity-summary-text";
        text.textContent = summaryData.text;
        header.append(text);
      }
      if (summaryData.errorCount > 0) {
        const error = this.document.createElement("span");
        error.className = "focus-activity-error-count";
        error.textContent = `${summaryData.errorCount} ${summaryData.errorCount === 1 ? "error" : "errors"}`;
        header.append(error);
      }
      summary.append(header);

      if (items.length > 0) {
        const details = this.document.createElement("div");
        details.className = "focus-activity-details";
        details.hidden = !expanded;
        const hiddenItemCount = Math.max(0, items.length - FOCUSED_ACTIVITY_ITEM_LIMIT);
        if (hiddenItemCount > 0) {
          const notice = this.document.createElement("p");
          notice.className = "focus-activity-hidden-count";
          notice.textContent = `… (${hiddenItemCount} previous ${hiddenItemCount === 1 ? "item" : "items"} hidden)`;
          details.append(notice);
        }
        const list = this.document.createElement("ul");
        list.className = "focus-activity-list";
        items.slice(-FOCUSED_ACTIVITY_ITEM_LIMIT).forEach((item) => {
          const row = this.document.createElement("li");
          row.className = `focus-activity-item focus-activity-item--${item.type}`;
          const marker = this.document.createElement("span");
          marker.className = "focus-activity-item-marker";
          marker.textContent = item.type === "error" ? "!" : "›";
          marker.setAttribute("aria-hidden", "true");
          const text = this.document.createElement("span");
          text.className = "focus-activity-item-text";
          text.textContent = item.text;
          row.append(marker, text);
          list.append(row);
        });
        details.append(list);
        summary.append(details);
      }
      group[0].before(summary);
      if (focusAnchor && group.includes(focusAnchor)) replacementFocus = header;
    });
    if (activeToggle && !replacementFocus) replacementFocus = this.viewSelectControl;
    replacementFocus?.focus({ preventScroll: true });
  }

  scheduleFocusedActivityRefresh() {
    if (!this.element || this.focusedActivityRefreshFrame) return;
    const epoch = this.bindingEpoch;
    const element = this.element;
    this.focusedActivityRefreshFrame = requestAnimationFrame(() => {
      this.focusedActivityRefreshFrame = null;
      if (epoch === this.bindingEpoch && element === this.element) this.refreshFocusedActivity();
    });
  }

  toggleFocusedActivity(toggle) {
    const summary = toggle.closest("[data-focus-activity-summary]");
    const expanded = toggle.getAttribute("aria-expanded") !== "true";
    toggle.setAttribute("aria-expanded", String(expanded));
    summary.classList.toggle("is-expanded", expanded);
    const details = summary.querySelector(".focus-activity-details");
    if (details) details.hidden = !expanded;
    this.updateJumpControls();
  }

  listen(target, type, listener, options) {
    if (!target) return;
    target.addEventListener(type, listener, options);
    this.listeners.push([target, type, listener, options]);
  }

  timeout(callback, delay) {
    const epoch = this.bindingEpoch;
    const element = this.element;
    const timer = setTimeout(() => {
      this.timers.delete(timer);
      if (epoch === this.bindingEpoch && element === this.element) callback();
    }, delay);
    this.timers.add(timer);
    return timer;
  }

  clearTimer(timer) {
    if (!timer) return;
    clearTimeout(timer);
    this.timers.delete(timer);
  }

  frame(callback) {
    const epoch = this.bindingEpoch;
    const element = this.element;
    const frame = requestAnimationFrame(() => {
      this.frames.delete(frame);
      if (epoch === this.bindingEpoch && element === this.element) callback();
    });
    this.frames.add(frame);
    return frame;
  }

  nearTop() {
    return !!this.element && this.element.scrollTop < 120;
  }

  nearBottom() {
    return !!this.element && this.element.scrollHeight - this.element.scrollTop - this.element.clientHeight < 120;
  }

  latestReadableAssistantMessage() {
    const messages = this.element?.querySelectorAll('[data-role="assistant"].message--assistant:not(.message--thinking):not(.message--compact)');
    return messages?.[messages.length - 1] || null;
  }

  latestMessageElement() {
    const messages = this.element?.querySelectorAll(".message");
    return messages?.[messages.length - 1] || null;
  }

  latestReadableAssistantMessageIsVisible() {
    const latestAssistant = this.latestReadableAssistantMessage();
    if (!this.element || !latestAssistant) return false;
    const scrollRect = this.element.getBoundingClientRect();
    const elementRect = latestAssistant.getBoundingClientRect();
    return elementRect.bottom > scrollRect.top && elementRect.top < scrollRect.bottom;
  }

  recordScrollIntent(intent) {
    this.scrollIntent = intent;
    if (intent !== "keyboard") this.messageJumpTargetsSuppressed = false;
  }

  handleScroll() {
    const currentScrollTop = this.element.scrollTop;
    if (!this.programmaticScroll) {
      if (currentScrollTop > this.lastScrollTop) this.scrollDirection = "down";
      if (currentScrollTop < this.lastScrollTop) this.scrollDirection = "up";
      this.autoScrollEnabled = this.nearBottom();
      this.forceBottomAutoScroll = false;
      this.followOversizedMessageBottom = this.autoScrollEnabled;
      this.updateJumpControlsReveal();
    }
    this.lastScrollTop = currentScrollTop;
    this.updateJumpControls();
    if (this.nearTop()) this.loadOlderWindow().catch(() => {});
  }

  updateJumpControlsReveal() {
    this.lastRevealAt = Date.now();
    if (this.document.body.classList.contains("is-conversation-scrolling")) {
      this.hideJumpControlsSoon();
      return;
    }
    if (this.revealDelayTimer) return;
    this.revealDelayTimer = this.timeout(() => {
      this.revealDelayTimer = null;
      if (Date.now() - this.lastRevealAt > 120) return;
      this.document.body.classList.add("is-conversation-scrolling");
      this.hideJumpControlsSoon();
    }, 300);
  }

  hideJumpControlsSoon() {
    this.clearTimer(this.revealTimer);
    this.revealTimer = this.timeout(() => {
      this.document.body.classList.remove("is-conversation-scrolling");
      this.revealTimer = null;
    }, 1400);
  }

  oversizedMessageJumpTarget(direction) {
    if (!this.element || this.messageJumpTargetsSuppressed) return null;
    const scrollRect = this.element.getBoundingClientRect();
    const tolerance = 2;
    return [...this.element.querySelectorAll(".message")].find((message) => {
      if (message.offsetHeight <= this.element.clientHeight + tolerance) return false;
      const messageRect = message.getBoundingClientRect();
      if (direction === "up") return messageRect.top < scrollRect.top - tolerance && messageRect.bottom > scrollRect.top + tolerance;
      return messageRect.bottom > scrollRect.bottom + tolerance && messageRect.top < scrollRect.bottom - tolerance;
    }) || null;
  }

  setJumpButton(button, target, label, ariaLabel) {
    if (!button) return;
    button.textContent = label;
    if (!button.classList.contains("is-loading")) button.setAttribute("aria-label", ariaLabel);
    button.dataset.jumpTarget = target;
  }

  updateJumpControls() {
    const messageTopTarget = this.scrollDirection === "up" ? this.oversizedMessageJumpTarget("up") : null;
    const messageBottomTarget = this.scrollDirection === "down" ? this.oversizedMessageJumpTarget("down") : null;
    const allowJumpButtons = this.scrollIntent !== "keyboard";
    const showFirst = allowJumpButtons && this.scrollDirection === "up" && !this.autoScrollEnabled && !this.nearTop();
    const showLatest = allowJumpButtons && this.scrollDirection === "down" && !this.nearBottom() && (!!messageBottomTarget || !this.latestReadableAssistantMessageIsVisible());
    this.setJumpButton(this.jumpToFirstButton, messageTopTarget ? "message" : "conversation", messageTopTarget ? "↑" : "↑↑", messageTopTarget ? "Message top" : "Top");
    this.setJumpButton(this.jumpToLatestButton, messageBottomTarget ? "message" : "conversation", messageBottomTarget ? "↓" : "↓↓", messageBottomTarget ? "Message bottom" : "Bottom");
    this.jumpToFirstButton?.classList.toggle("is-visible", showFirst);
    this.jumpToLatestButton?.classList.toggle("is-visible", showLatest);
    this.topJumpControls?.classList.toggle("is-visible", showFirst);
    this.bottomJumpControls?.classList.toggle("is-visible", showLatest);
  }

  withProgrammaticScroll(callback) {
    this.programmaticScroll = true;
    callback();
    this.timeout(() => { this.programmaticScroll = false; }, 120);
  }

  scrollElementTopIntoView(element, behavior = "smooth", topOffset = 0) {
    if (!this.element || !element) return;
    const scrollRect = this.element.getBoundingClientRect();
    const elementRect = element.getBoundingClientRect();
    const top = this.element.scrollTop + elementRect.top - scrollRect.top - topOffset;
    this.element.scrollTo({ top, behavior });
  }

  scrollElementBottomIntoView(element, behavior = "smooth", bottomOffset = 0) {
    if (!this.element || !element) return;
    const scrollRect = this.element.getBoundingClientRect();
    const elementRect = element.getBoundingClientRect();
    const top = this.element.scrollTop + elementRect.bottom - scrollRect.bottom + bottomOffset;
    this.element.scrollTo({ top, behavior });
  }

  positionInitialAtBottom() {
    if (!this.element) return;
    this.autoScrollEnabled = true;
    this.forceBottomAutoScroll = false;
    this.followOversizedMessageBottom = false;
    this.element.scrollTop = this.element.scrollHeight;
    this.lastScrollTop = this.element.scrollTop;
    this.updateJumpControls();
  }

  currentSessionPath() {
    return this.document.querySelector('.prompt-form input[name="session"]')?.value || new URLSearchParams(this.window.location.search).get("session") || "";
  }

  olderConversationUrl(cursor, element = this.element, afterCursor = null) {
    const url = new URL(element.dataset.olderMessagesUrl, this.window.location.origin);
    url.searchParams.set("cursor", cursor);
    if (afterCursor !== null) url.searchParams.set("after", afterCursor);
    return url;
  }

  historyStatus() {
    return this.element?.querySelector?.("[data-conversation-history-status]");
  }

  observeHistoryStatus() {
    const status = this.historyStatus();
    if (!status || !this.window.IntersectionObserver) return;
    const loadVisibleHistory = () => this.loadOlderWindow().then((result) => {
      if (result !== "more" || status !== this.historyStatus()) return result;
      const statusRect = status.getBoundingClientRect();
      const scrollRect = this.element.getBoundingClientRect();
      if (statusRect.bottom > scrollRect.top && statusRect.top < scrollRect.bottom) return loadVisibleHistory();
      return result;
    });
    this.historyIntersectionObserver = new this.window.IntersectionObserver((entries) => {
      if (status !== this.historyStatus() || !entries.some((entry) => entry.target === status && entry.isIntersecting)) return;
      loadVisibleHistory().catch(() => {});
    }, { root: this.element });
    this.historyIntersectionObserver.observe(status);
  }

  setHistoryStatus(text, hidden = false) {
    const status = this.historyStatus();
    if (!status) return;
    status.hidden = hidden;
    status.disabled = text === "Loading earlier messages…";
    status.textContent = text;
  }

  finishHistoryStatus() {
    this.setHistoryStatus("", true);
  }

  availableHistoryStatus() {
    this.setHistoryStatus("Earlier messages available");
  }

  loadingHistoryStatus() {
    this.setHistoryStatus("Loading earlier messages…");
  }

  failHistoryStatus() {
    this.setHistoryStatus("Earlier messages could not load.");
  }

  insertHistoryHtml(html, insertionPoint, preserveViewport) {
    if (!this.element || !html) return;
    const template = this.document.createElement("template");
    template.innerHTML = html;
    enhanceMarkdownCodeBlocks(template.content, this.document);
    const element = this.element;
    const bindingEpoch = this.bindingEpoch;
    const historyRequestGeneration = this.historyRequestGeneration;
    const insert = () => {
      if (element !== this.element || bindingEpoch !== this.bindingEpoch || historyRequestGeneration !== this.historyRequestGeneration) return;
      const previousTop = element.scrollTop;
      const previousHeight = element.scrollHeight;
      element.insertBefore(template.content, insertionPoint);
      this.refreshFocusedActivity();
      if (preserveViewport) element.scrollTop = previousTop + (element.scrollHeight - previousHeight);
      this.lastScrollTop = element.scrollTop;
      this.updateJumpControls();
    };
    const enhancement = this.historyEnhancer?.(template.content);
    if (enhancement?.then) return enhancement.catch(() => {}).then(insert);
    insert();
  }

  prependOlderHtml(html) {
    const insertionPoint = this.element?.querySelector(".message") || this.liveOutput || this.element?.firstElementChild;
    return this.insertHistoryHtml(html, insertionPoint, true);
  }

  historyGapAboveViewport() {
    const gapTop = this.historyStatus()?.getBoundingClientRect?.().top;
    const viewportTop = this.element?.getBoundingClientRect?.().top;
    return gapTop !== undefined && viewportTop !== undefined && gapTop < viewportTop;
  }

  insertBeforeHistoryGap(html, preserveViewport = false) {
    const insertionPoint = this.historyStatus() || this.element?.querySelector(".message") || this.liveOutput || this.element?.firstElementChild;
    return this.insertHistoryHtml(html, insertionPoint, preserveViewport);
  }

  loadOlderWindow({ afterCursor = null } = {}) {
    if (!this.element) return Promise.resolve("cancelled");
    if (this.olderWindowPromise) return this.olderWindowPromise;
    const cursor = Number(this.element.dataset.olderMessageCursor || 0);
    if (!cursor || this.element.dataset.hasOlderMessages !== "true") return Promise.resolve("complete");
    if (afterCursor === null && this.element.dataset.oldestMessageEndCursor !== undefined) afterCursor = Number(this.element.dataset.oldestMessageEndCursor);

    const epoch = this.bindingEpoch;
    const historyGeneration = this.historyRequestGeneration;
    const scrollElement = this.element;
    const sessionPath = this.currentSessionPath();
    const abortController = new AbortController();
    this.historyAbortController = abortController;
    const unchanged = () => epoch === this.bindingEpoch && historyGeneration === this.historyRequestGeneration && scrollElement === this.element && sessionPath === this.currentSessionPath();
    this.loadingHistoryStatus();
    const load = (async () => {
      try {
        const response = await fetch(this.olderConversationUrl(cursor, scrollElement, afterCursor), { headers: { "Accept": "application/json" }, signal: abortController.signal });
        if (!unchanged()) return "cancelled";
        if (!response.ok) {
          this.failHistoryStatus();
          return "failed";
        }
        const payload = await response.json();
        if (!unchanged()) return "cancelled";
        const nextCursor = Number(payload.next_cursor || 0);
        const forward = afterCursor !== null;
        if (payload.has_older_messages && (forward ? nextCursor <= afterCursor || nextCursor > cursor : nextCursor >= cursor)) {
          this.failHistoryStatus();
          return "failed";
        }

        const preserveGapViewport = forward && this.historyGapAboveViewport();
        if (forward) await this.insertBeforeHistoryGap(payload.html || "", preserveGapViewport);
        else await this.prependOlderHtml(payload.html || "");
        if (!unchanged()) return "cancelled";

        if (forward) scrollElement.dataset.oldestMessageEndCursor = String(nextCursor);
        else scrollElement.dataset.olderMessageCursor = String(nextCursor);
        scrollElement.dataset.hasOlderMessages = payload.has_older_messages ? "true" : "false";
        scrollElement.dataset.olderMessageCount = String(payload.older_message_count || 0);
        if (payload.has_older_messages) this.availableHistoryStatus();
        else this.finishHistoryStatus();
        return payload.has_older_messages ? "more" : "complete";
      } catch (error) {
        if (!unchanged() || error?.name === "AbortError") return "cancelled";
        this.failHistoryStatus();
        return "failed";
      }
    })();
    const shared = load.finally(() => {
      if (this.olderWindowPromise === shared) {
        this.olderWindowPromise = null;
        this.historyWindowAfterCursor = null;
      }
      if (this.historyAbortController === abortController) this.historyAbortController = null;
    });
    this.olderWindowPromise = shared;
    this.historyWindowAfterCursor = afterCursor;
    return shared;
  }

  loadOldestWindow() {
    if (!this.element || this.element.dataset.oldestMessageEndCursor !== undefined) return Promise.resolve("complete");
    if (this.olderWindowPromise && this.historyWindowAfterCursor === 0) return this.olderWindowPromise;
    if (this.olderWindowPromise) this.cancelOlderHistory();
    return this.loadOlderWindow({ afterCursor: 0 });
  }

  cancelOlderHistory() {
    this.historyRequestGeneration += 1;
    this.historyAbortController?.abort();
    this.historyAbortController = null;
    this.olderWindowPromise = null;
    this.historyWindowAfterCursor = null;
    this.olderHistoryPromise = null;
    if (this.element?.dataset.hasOlderMessages === "true") this.availableHistoryStatus();
  }

  get olderHistoryLoading() {
    return !!this.olderHistoryPromise;
  }

  loadOlderHistory() {
    if (!this.element) return Promise.resolve("cancelled");
    if (this.olderHistoryPromise) return this.olderHistoryPromise;
    if (this.olderWindowPromise) this.cancelOlderHistory();
    const load = (async () => {
      let status;
      do {
        status = await this.loadOlderWindow();
      } while (status === "more");
      return status;
    })();
    const shared = load.finally(() => {
      if (this.olderHistoryPromise === shared) this.olderHistoryPromise = null;
    });
    this.olderHistoryPromise = shared;
    return shared;
  }

  applyAutoScroll(behavior = "auto") {
    if (!this.element || !this.autoScrollEnabled) return;
    this.withProgrammaticScroll(() => {
      const latestAssistant = this.latestReadableAssistantMessage();
      if (!this.forceBottomAutoScroll && !this.followOversizedMessageBottom && latestAssistant && latestAssistant === this.latestMessageElement() && latestAssistant.offsetHeight > this.element.clientHeight) {
        this.scrollElementTopIntoView(latestAssistant, behavior);
      } else {
        this.element.scrollTo({ top: this.element.scrollHeight, behavior });
      }
    });
    this.updateJumpControls();
  }

  scheduleAutoScroll(behavior = "auto") {
    if (!this.element || !this.autoScrollEnabled) return;
    this.frames.forEach((frame) => cancelAnimationFrame(frame));
    this.frames.clear();
    this.frame(() => this.frame(() => this.applyAutoScroll(behavior)));
  }

  jumpToConversationTop() {
    const button = this.jumpToFirstButton;
    if (!button) return Promise.resolve();
    const controls = this.topJumpControls;
    button.classList.add("is-loading");
    controls?.classList.add("is-loading");
    button.disabled = true;
    button.setAttribute("aria-busy", "true");
    button.setAttribute("aria-label", "Loading earlier messages");
    return this.loadOldestWindow()
      .then((status) => { if (status === "complete" || status === "more") this.scrollToTop("auto"); })
      .catch(() => {})
      .finally(() => {
        button.classList.remove("is-loading");
        controls?.classList.remove("is-loading");
        button.disabled = false;
        button.removeAttribute("aria-busy");
        if (button === this.jumpToFirstButton) this.updateJumpControls();
      });
  }

  scrollToTop(behavior = "smooth") {
    if (!this.element) return;
    this.autoScrollEnabled = false;
    this.forceBottomAutoScroll = false;
    this.followOversizedMessageBottom = false;
    this.withProgrammaticScroll(() => this.element.scrollTo({ top: 0, behavior }));
    this.updateJumpControls();
  }

  clearMessageJumpSuppressionScrollEndListener() {
    if (!this.messageJumpSuppressionScrollEndListener) return;
    this.element?.removeEventListener("scrollend", this.messageJumpSuppressionScrollEndListener);
    this.messageJumpSuppressionScrollEndListener = null;
  }

  suppressMessageJumpTargetsDuringScroll() {
    this.messageJumpTargetsSuppressed = true;
    const generation = ++this.messageJumpSuppressionGeneration;
    this.clearMessageJumpSuppressionScrollEndListener();
    this.setJumpButton(this.jumpToFirstButton, "conversation", "↑↑", "Top");
    this.setJumpButton(this.jumpToLatestButton, "conversation", "↓↓", "Bottom");
    this.updateJumpControls();
    this.clearTimer(this.messageJumpSuppressionTimer);
    const release = () => {
      if (generation !== this.messageJumpSuppressionGeneration) return;
      this.messageJumpTargetsSuppressed = false;
      this.clearTimer(this.messageJumpSuppressionTimer);
      this.messageJumpSuppressionTimer = null;
      this.clearMessageJumpSuppressionScrollEndListener();
      this.updateJumpControls();
    };
    if ("onscrollend" in this.window) {
      this.messageJumpSuppressionScrollEndListener = release;
      this.element.addEventListener("scrollend", release, { once: true });
    }
    this.messageJumpSuppressionTimer = this.timeout(release, 1200);
  }

  scrollToMessageTop(behavior = "smooth") {
    const target = this.oversizedMessageJumpTarget("up");
    if (!target) return this.scrollToTop(behavior);
    this.autoScrollEnabled = false;
    this.forceBottomAutoScroll = false;
    this.followOversizedMessageBottom = false;
    this.suppressMessageJumpTargetsDuringScroll();
    this.withProgrammaticScroll(() => this.scrollElementTopIntoView(target, behavior));
  }

  scrollToMessageBottom(behavior = "smooth") {
    const target = this.oversizedMessageJumpTarget("down");
    if (!target) return this.scrollToBottom(behavior, { force: true });
    const latestAssistant = this.latestReadableAssistantMessage();
    const latestOversizedAssistant = target === latestAssistant && target === this.latestMessageElement();
    this.autoScrollEnabled = latestOversizedAssistant;
    this.forceBottomAutoScroll = false;
    this.followOversizedMessageBottom = latestOversizedAssistant;
    this.suppressMessageJumpTargetsDuringScroll();
    this.withProgrammaticScroll(() => this.scrollElementBottomIntoView(target, behavior));
  }

  scrollToBottom(behavior = "auto", { force = false } = {}) {
    if (!this.element) return;
    this.autoScrollEnabled = true;
    this.forceBottomAutoScroll = force;
    this.followOversizedMessageBottom = true;
    this.withProgrammaticScroll(() => this.element.scrollTo({ top: this.element.scrollHeight, behavior }));
    this.updateJumpControls();
  }

  followLiveOutput(forceScroll = false) {
    const shouldScroll = forceScroll || this.autoScrollEnabled || this.nearBottom();
    if (shouldScroll) this.autoScrollEnabled = true;
    return shouldScroll;
  }

  afterLiveOutputChange(shouldScroll, live = true, activityChanged = false) {
    if (activityChanged) this.scheduleFocusedActivityRefresh();
    if (shouldScroll && this.autoScrollEnabled) this.scheduleAutoScroll();
    else if (live) this.updateJumpControls();
  }

  stopAutoFollow() {
    this.autoScrollEnabled = false;
    this.forceBottomAutoScroll = false;
    this.followOversizedMessageBottom = false;
  }

  resetOversizedFollow() {
    this.forceBottomAutoScroll = false;
    this.followOversizedMessageBottom = false;
  }

  forceInitialBottomFollow() {
    this.forceBottomAutoScroll = true;
    this.followOversizedMessageBottom = true;
    this.applyAutoScroll("auto");
    this.forceBottomAutoScroll = false;
  }
}
