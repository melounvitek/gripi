import { enhanceMarkdownCodeBlocks } from "./dom.js";

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
  }

  bind(promptTextarea = null) {
    this.detach();
    this.bindingEpoch += 1;
    this.element = this.document.getElementById("conversation-scroll");
    this.liveOutput = this.document.getElementById("live-output");
    this.promptTextarea = promptTextarea;
    this.topJumpControls = this.document.querySelector(".jump-controls--top");
    this.bottomJumpControls = this.document.querySelector(".jump-controls--bottom");
    this.jumpToFirstButton = this.document.querySelector(".jump-to-first");
    this.jumpToLatestButton = this.document.querySelector(".jump-to-latest");
    this.lastScrollTop = this.element?.scrollTop || 0;
    this.scrollDirection = null;
    this.followOversizedMessageBottom = false;
    if (!this.element) return;

    this.listen(this.element, "keydown", (event) => {
      if (event.key !== "Tab" || !this.promptTextarea || this.window.matchMedia?.("(max-width: 760px)").matches === true) return;
      event.preventDefault();
      this.promptTextarea.focus({ preventScroll: true });
    });
    ["wheel", "touchstart", "pointerdown"].forEach((type) => {
      this.listen(this.element, type, () => this.recordScrollIntent("pointer"), { passive: true });
    });
    this.listen(this.element, "scroll", () => this.handleScroll(), { passive: true });
    this.listen(this.jumpToFirstButton, "click", () => {
      if (this.jumpToFirstButton.dataset.jumpTarget === "message") this.scrollToMessageTop();
      else this.scrollToTop();
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
    this.olderHistoryPromise = null;
    this.revealTimer = null;
    this.revealDelayTimer = null;
    this.messageJumpSuppressionTimer = null;
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
    this.autoScrollEnabled = true;
    this.forceBottomAutoScroll = false;
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
    button.setAttribute("aria-label", ariaLabel);
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

  olderConversationUrl(cursor, element = this.element) {
    const url = new URL(element.dataset.olderMessagesUrl, this.window.location.origin);
    url.searchParams.set("cursor", cursor);
    return url;
  }

  historyStatus() {
    return this.element?.querySelector("[data-conversation-history-status]");
  }

  finishHistoryStatus() {
    const status = this.historyStatus();
    if (status) status.hidden = true;
  }

  failHistoryStatus() {
    const status = this.historyStatus();
    if (!status) return;
    status.hidden = false;
    status.textContent = "Earlier messages could not load.";
  }

  prependOlderHtml(html) {
    if (!this.element || !html) return;
    const template = this.document.createElement("template");
    template.innerHTML = html;
    enhanceMarkdownCodeBlocks(template.content, this.document);
    const previousTop = this.element.scrollTop;
    const previousHeight = this.element.scrollHeight;
    const insertionPoint = this.element.querySelector(".message") || this.liveOutput || this.element.firstElementChild;
    this.element.insertBefore(template.content, insertionPoint);
    this.element.scrollTop = previousTop + (this.element.scrollHeight - previousHeight);
    this.lastScrollTop = this.element.scrollTop;
    this.updateJumpControls();
  }

  loadOlderHistory() {
    if (!this.element) return Promise.resolve("cancelled");
    if (this.olderHistoryPromise) return this.olderHistoryPromise;
    let cursor = Number(this.element.dataset.olderMessageCursor || 0);
    if (!cursor || this.element.dataset.hasOlderMessages !== "true") return Promise.resolve("complete");

    const epoch = this.bindingEpoch;
    const scrollElement = this.element;
    const sessionPath = this.currentSessionPath();
    const abortController = new AbortController();
    this.historyAbortController = abortController;
    const unchanged = () => epoch === this.bindingEpoch && scrollElement === this.element && sessionPath === this.currentSessionPath();
    const load = (async () => {
      try {
        while (cursor > 0) {
          if (!unchanged()) return "cancelled";
          const response = await fetch(this.olderConversationUrl(cursor, scrollElement), { headers: { "Accept": "application/json" }, signal: abortController.signal });
          if (!unchanged()) return "cancelled";
          if (!response.ok) {
            this.failHistoryStatus();
            return "failed";
          }
          const payload = await response.json();
          if (!unchanged()) return "cancelled";
          this.prependOlderHtml(payload.html || "");
          cursor = Number(payload.next_cursor || 0);
          scrollElement.dataset.olderMessageCursor = String(cursor);
          scrollElement.dataset.hasOlderMessages = payload.has_older_messages ? "true" : "false";
          scrollElement.dataset.olderMessageCount = String(payload.older_message_count || 0);
          if (!payload.has_older_messages) {
            this.finishHistoryStatus();
            break;
          }
        }
        return "complete";
      } catch (error) {
        if (!unchanged() || error?.name === "AbortError") return "cancelled";
        this.failHistoryStatus();
        return "failed";
      }
    })();
    const shared = load.finally(() => {
      if (this.olderHistoryPromise === shared) this.olderHistoryPromise = null;
      if (this.historyAbortController === abortController) this.historyAbortController = null;
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

  afterLiveOutputChange(shouldScroll, live = true) {
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
