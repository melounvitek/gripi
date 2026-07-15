import { notificationReplyPreview } from "./formatting.js";
import { newSessionModalUrl, sessionUrl } from "./urls.js";

export class SidebarController {
  constructor(document, window, projectSelectController, gatewayUpdateController, notifyFinalReply) {
    this.document = document;
    this.window = window;
    this.projectSelectController = projectSelectController;
    this.gatewayUpdateController = gatewayUpdateController;
    this.notifyFinalReply = notifyFinalReply;
    this.element = null;
    this.refreshTimer = null;
    this.asyncEpoch = 0;
    this.refreshRequestVersion = 0;
    this.lastInteractionAt = 0;
    this.temporarySessionsLimit = null;
    this.pinOperationActive = false;
    this.notifiedFinalReplyKeys = new Set();
    this.listenersBound = false;
  }

  initialize() {
    this.bindPageListeners();
    this.bind();
  }

  bind(element = this.document.querySelector(".session-sidebar")) {
    this.invalidate();
    this.element = element;
    if (!this.element) return null;

    this.projectSelectController.initialize(this.element);
    this.bindInteractionTracking();
    this.syncMobileUnreadBadges();
    this.gatewayUpdateController.apply();
    return this.element;
  }

  invalidate({ clearSessionsLimit = false } = {}) {
    this.asyncEpoch += 1;
    clearTimeout(this.refreshTimer);
    this.refreshTimer = null;
    if (clearSessionsLimit) this.temporarySessionsLimit = null;
  }

  pause() {
    this.invalidate();
  }

  bindPageListeners() {
    if (this.listenersBound) return;
    this.listenersBound = true;

    this.document.addEventListener("change", (event) => {
      const select = event.target.closest?.("[data-sidebar-project-filter]");
      if (!select) return;
      this.projectSelectController.sync(select);
      this.changeProjectFilter(select).catch(() => select.form?.submit());
    });
    this.document.addEventListener("submit", (event) => {
      if (event.target.closest?.(".sidebar-session-search")) this.setFiltering(true);
    });
    this.document.addEventListener("click", (event) => {
      if (event.target.closest?.("[data-sidebar-filters-clear]")) this.setFiltering(true);

      const pinButton = event.target.closest?.("[data-session-pin-toggle]");
      if (pinButton) {
        event.preventDefault();
        this.togglePin(pinButton).catch(() => {});
        return;
      }

      const searchButton = event.target.closest?.("[data-sidebar-search-toggle]");
      if (searchButton) this.toggleSearch(searchButton);

      const loadMoreButton = event.target.closest?.("[data-sidebar-load-more]");
      if (!loadMoreButton || !this.normalLeftClick(event)) return;
      event.preventDefault();
      this.loadMore(loadMoreButton);
    });
  }

  bindInteractionTracking() {
    const scrollContainer = this.scrollContainer();
    if (!scrollContainer) return;
    const track = () => { this.lastInteractionAt = Date.now(); };
    scrollContainer.onscroll = track;
    scrollContainer.onwheel = track;
    scrollContainer.ontouchstart = track;
    scrollContainer.onpointerdown = track;
  }

  recentlyInteracted() {
    return Date.now() - this.lastInteractionAt < 1000;
  }

  scrollContainer() {
    return this.element?.querySelector(".session-sidebar-content") || this.element;
  }

  fragmentUrl(url = this.window.location.href) {
    const target = new URL(url, this.window.location.href);
    if (url === this.window.location.href && this.temporarySessionsLimit && !target.searchParams.has("sidebar_sessions_limit")) {
      target.searchParams.set("sidebar_sessions_limit", this.temporarySessionsLimit);
    }
    const sidebarUrl = new URL("/sidebar", this.window.location.origin);
    target.searchParams.forEach((value, key) => sidebarUrl.searchParams.append(key, value));
    if (!sidebarUrl.searchParams.has("session")) {
      const selectedSession = this.element?.querySelector("a.session.selected[data-session-path]")?.dataset.sessionPath;
      if (selectedSession) sidebarUrl.searchParams.set("session", selectedSession);
    }
    return sidebarUrl;
  }

  scheduleRefresh(delay = this.refreshDelay()) {
    clearTimeout(this.refreshTimer);
    this.refreshTimer = null;
    if (!this.element || this.document.hidden || this.modalIsOpen()) return;
    this.refreshTimer = setTimeout(() => this.refresh().catch(() => {}), delay);
  }

  refreshDelay() {
    const active = this.element?.querySelector(".session-running-indicator, .session-compacting-indicator");
    return active ? 2500 : 10000;
  }

  requestRefresh(delay = 0) {
    if (!this.pinOperationActive) this.invalidate();
    this.refreshRequestVersion += 1;
    this.scheduleRefresh(delay);
  }

  async refresh({ force = false } = {}) {
    if (!this.element || (!force && this.modalIsOpen())) return;
    if (!force && (this.pinOperationActive || this.controlsActive() || this.recentlyInteracted())) {
      this.scheduleRefresh(1000);
      return;
    }

    const epoch = ++this.asyncEpoch;
    const boundElement = this.element;
    try {
      const response = await fetch(this.fragmentUrl());
      if (!this.current(epoch, boundElement)) return;
      if (!response.ok) {
        this.scheduleRefresh();
        return;
      }
      const html = await response.text();
      if (!this.current(epoch, boundElement)) return;
      if (!force && this.controlsActive()) {
        this.scheduleRefresh(1000);
        return;
      }

      this.replace(html, { scrollTop: this.scrollContainer()?.scrollTop || 0 });
      this.scheduleRefresh();
    } catch (error) {
      if (this.current(epoch, boundElement)) this.scheduleRefresh();
      throw error;
    }
  }

  replace(html, { scrollTop = this.scrollContainer()?.scrollTop || 0, notify = true } = {}) {
    if (!html || !this.element) return null;

    const oldElement = this.element;
    const previousAssistantCounts = this.assistantResponseCounts(oldElement);
    const notificationToggle = oldElement.querySelector("[data-notification-toggle]");
    const previousSearchForm = oldElement.querySelector(".sidebar-session-search");
    const previousSearchQuery = previousSearchForm?.querySelector('input[name="session_search"]')?.value;
    const previousSearchOpen = previousSearchForm?.classList.contains("is-open");
    const focusedPinPath = this.document.activeElement?.closest?.("[data-session-pin-toggle]")?.dataset.sessionPath;
    this.projectSelectController.destroy(oldElement);
    oldElement.outerHTML = html;
    this.bind(this.document.querySelector(".session-sidebar"));
    if (!this.element) return null;

    const replacementSearchForm = this.element.querySelector(".sidebar-session-search");
    const replacementSearchInput = replacementSearchForm?.querySelector('input[name="session_search"]');
    const replacementSearchButton = this.element.querySelector("[data-sidebar-search-toggle]");
    if (replacementSearchInput && previousSearchQuery !== undefined) replacementSearchInput.value = previousSearchQuery;
    if (previousSearchOpen !== undefined) this.setSearchOpen(replacementSearchForm, replacementSearchButton, previousSearchOpen);
    if (notificationToggle) this.element.querySelector("[data-notification-toggle]")?.replaceWith(notificationToggle);
    if (focusedPinPath) {
      const focusedPin = [...this.element.querySelectorAll("[data-session-pin-toggle]")].find((button) => button.dataset.sessionPath === focusedPinPath);
      (focusedPin || this.element.querySelector("[data-sidebar-search-toggle]"))?.focus({ preventScroll: true });
    }
    if (notify) this.notifyBackgroundFinalReplies(previousAssistantCounts);
    const refreshedScrollContainer = this.scrollContainer();
    if (refreshedScrollContainer) refreshedScrollContainer.scrollTop = scrollTop;

    const title = this.element.querySelector("a.session.selected .session-title")?.textContent.trim();
    if (title) this.document.dispatchEvent(new this.window.CustomEvent("gripi:sidebar-selected-title", { detail: { title } }));
    return title || null;
  }

  assistantResponseCounts(root = this.element) {
    const counts = new Map();
    root?.querySelectorAll("a.session[data-session-path][data-assistant-response-count]").forEach((link) => {
      const sessionPath = link.dataset.sessionPath;
      const count = Number(link.dataset.assistantResponseCount || 0);
      counts.set(sessionPath, Math.max(counts.get(sessionPath) || 0, count));
    });
    return counts;
  }

  notifyBackgroundFinalReplies(previousAssistantCounts) {
    this.element?.querySelectorAll("a.session[data-session-path][data-assistant-response-count]").forEach((link) => {
      const sessionPath = link.dataset.sessionPath;
      const previousCount = previousAssistantCounts.get(sessionPath);
      const currentCount = Number(link.dataset.assistantResponseCount || 0);
      if (previousCount == null || currentCount <= previousCount || sessionPath === this.currentSessionPath()) return;

      const key = `${sessionPath}:${currentCount}`;
      if (this.notifiedFinalReplyKeys.has(key)) return;
      this.notifiedFinalReplyKeys.add(key);
      const name = link.querySelector(".session-title")?.textContent.trim() || "a background session";
      this.notifyFinalReply(name, notificationReplyPreview(link.dataset.latestAssistantResponsePreview), sessionUrl(sessionPath), `gripi-final-reply:${sessionPath}`);
    });
  }

  async changeProjectFilter(select) {
    if (!select) return null;

    this.setFiltering(true);
    const targetUrl = new URL(this.window.location.href);
    if (select.value) targetUrl.searchParams.set("project", select.value);
    else targetUrl.searchParams.delete("project");
    targetUrl.searchParams.delete("sidebar_sessions_limit");
    this.temporarySessionsLimit = null;

    const epoch = ++this.asyncEpoch;
    const boundElement = this.element;
    const [sidebarResponse, modalResponse] = await Promise.all([
      fetch(this.fragmentUrl(targetUrl.href)),
      fetch(newSessionModalUrl(targetUrl.href))
    ]);
    if (!this.current(epoch, boundElement)) return null;
    if (!sidebarResponse.ok || !modalResponse.ok) throw new Error("Project filter refresh failed");
    const [html, modalHtml] = await Promise.all([sidebarResponse.text(), modalResponse.text()]);
    if (!this.current(epoch, boundElement)) return null;

    this.replace(html, { scrollTop: 0, notify: false });
    this.document.dispatchEvent(new this.window.CustomEvent("gripi:sidebar-project-filtered", { detail: { modalHtml } }));
    this.window.history.pushState(this.window.history.state, "", targetUrl.href);
    this.scheduleRefresh();
    return modalHtml;
  }

  async loadMore(button) {
    if (!button || button.classList.contains("is-loading")) return;

    const epoch = ++this.asyncEpoch;
    const boundElement = this.element;
    const previousScrollTop = this.scrollContainer()?.scrollTop || 0;
    const originalLabel = button.querySelector(".sidebar-load-more-label")?.textContent || "Load more";
    button.classList.add("is-loading");
    button.setAttribute("aria-busy", "true");
    const label = button.querySelector(".sidebar-load-more-label");
    if (label) label.textContent = "Loading…";

    try {
      const targetUrl = new URL(button.href, this.window.location.href);
      this.temporarySessionsLimit = targetUrl.searchParams.get("sidebar_sessions_limit") || this.temporarySessionsLimit;
      const response = await fetch(this.fragmentUrl(targetUrl.href));
      if (!response.ok || !this.current(epoch, boundElement)) return;
      const html = await response.text();
      if (!this.current(epoch, boundElement)) return;
      this.replace(html, { scrollTop: previousScrollTop });
      this.scheduleRefresh();
    } catch (_error) {
    } finally {
      if (this.current(epoch, boundElement)) this.scheduleRefresh();
      if (button.isConnected) {
        button.classList.remove("is-loading");
        button.removeAttribute("aria-busy");
        const currentLabel = button.querySelector(".sidebar-load-more-label");
        if (currentLabel) currentLabel.textContent = originalLabel;
      }
    }
  }

  async togglePin(button) {
    if (!button || button.disabled || this.pinOperationActive) return null;

    this.pinOperationActive = true;
    this.invalidate();
    const epoch = this.asyncEpoch;
    const boundElement = this.element;
    const currentlyPinned = button.dataset.pinned === "true";
    let idleLabel = currentlyPinned ? "Unpin session" : "Pin session";
    const loadingLabel = currentlyPinned ? "Unpinning session" : "Pinning session";
    button.disabled = true;
    button.classList.add("is-loading");
    button.setAttribute("aria-busy", "true");
    button.setAttribute("aria-label", loadingLabel);
    button.setAttribute("title", loadingLabel);
    try {
      const body = new URLSearchParams({
        session: button.dataset.sessionPath,
        pinned: currentlyPinned ? "false" : "true"
      });
      const response = await fetch("/sessions/pin", {
        method: "POST",
        body,
        headers: { "Accept": "application/json" }
      });
      if (!response.ok) throw new Error("Could not update pinned session");

      const payload = await response.json();
      if (this.current(epoch, boundElement) && button.isConnected !== false) {
        const pinned = payload.pinned === true;
        idleLabel = pinned ? "Unpin session" : "Pin session";
        button.dataset.pinned = pinned ? "true" : "false";
        button.classList.toggle("is-pinned", pinned);
        button.setAttribute("aria-pressed", pinned ? "true" : "false");
      }
      await this.refresh({ force: true });
      return payload;
    } catch (error) {
      if (this.current(epoch, boundElement)) this.scheduleRefresh();
      throw error;
    } finally {
      this.pinOperationActive = false;
      if (button.isConnected !== false) {
        button.disabled = false;
        button.classList.remove("is-loading");
        button.removeAttribute("aria-busy");
        button.setAttribute("aria-label", idleLabel);
        button.setAttribute("title", idleLabel);
      }
    }
  }

  markSessionCompacting(sessionPath) {
    if (!sessionPath || !this.element) return;
    const link = this.element.querySelector(`a.session[data-session-path="${CSS.escape(sessionPath)}"]`);
    if (!link) return;
    link.classList.add("compacting");
    this.scheduleRefresh(2500);
    const indicators = link.querySelector(".session-indicators");
    if (!indicators || indicators.querySelector(".session-compacting-indicator")) return;
    indicators.querySelector(".session-running-indicator")?.remove();
    const indicator = this.document.createElement("span");
    indicator.className = "session-compacting-indicator";
    indicator.title = "Compacting context";
    indicator.setAttribute("aria-label", "Compacting context");
    indicators.appendChild(indicator);
  }

  syncMobileUnreadBadges() {
    const count = Number(this.element?.dataset.unreadSessionCount || 0);
    const label = `${count} unread ${count === 1 ? "session" : "sessions"}`;
    const text = count > 99 ? "99+" : String(count);
    this.document.querySelectorAll(".mobile-sessions-button").forEach((button) => {
      let badge = button.querySelector(".mobile-sessions-unread-badge");
      if (count <= 0) {
        badge?.remove();
        return;
      }
      if (!badge) {
        badge = this.document.createElement("span");
        badge.className = "mobile-sessions-unread-badge";
        button.append(badge);
      }
      badge.textContent = text;
      badge.setAttribute("aria-label", label);
      badge.setAttribute("title", label);
    });
  }

  setSearchOpen(form, button, open) {
    const input = form?.querySelector('input[name="session_search"]');
    if (!form || !input || !button) return false;
    const projectSelect = form.closest(".recent-sessions")?.querySelector("[data-sidebar-project-filter]");
    form.classList.toggle("is-open", open);
    button.classList.toggle("is-active", open || input.value.trim() !== "" || projectSelect?.value !== "");
    button.setAttribute("aria-expanded", open ? "true" : "false");
    return true;
  }

  openSearch() {
    const form = this.element?.querySelector(".sidebar-session-search");
    const input = form?.querySelector('input[name="session_search"]');
    const button = this.element?.querySelector("[data-sidebar-search-toggle]");
    if (!this.setSearchOpen(form, button, true)) return false;
    const mobileToggle = this.document.getElementById("mobile-session-toggle");
    if (mobileToggle) mobileToggle.checked = true;
    input.focus({ preventScroll: true });
    input.select();
    return true;
  }

  closeSearch(event) {
    if (event.key !== "Escape") return false;
    const form = this.document.activeElement?.closest?.(".sidebar-session-search") || event.target?.closest?.(".sidebar-session-search");
    if (!form?.classList.contains("is-open")) return false;
    const button = form.closest(".session-sidebar")?.querySelector("[data-sidebar-search-toggle]");
    if (!this.setSearchOpen(form, button, false)) return false;
    this.closeMobile();
    event.preventDefault();
    event.stopPropagation?.();
    event.stopImmediatePropagation?.();
    return true;
  }

  toggleSearch(button) {
    const container = button.closest(".recent-sessions");
    const form = container?.querySelector(".sidebar-session-search");
    const input = form?.querySelector('input[name="session_search"]');
    if (!form || !input) return;
    const open = !form.classList.contains("is-open");
    if (this.setSearchOpen(form, button, open) && open) input.focus();
  }

  closeMobile() {
    const toggle = this.document.getElementById("mobile-session-toggle");
    if (toggle) toggle.checked = false;
  }

  setFiltering(filtering) {
    this.element?.classList.toggle("is-filtering", filtering);
    const trigger = this.element?.querySelector("[data-sidebar-project-filter]")?.closest("[data-project-select]")?._projectSelectState?.trigger;
    if (!trigger) return;
    trigger.disabled = filtering;
    trigger.setAttribute("aria-busy", filtering ? "true" : "false");
  }

  controlsActive() {
    return this.projectSelectController.isActive(this.element) || !!this.document.activeElement?.closest?.(".sidebar-session-search") || this.document.body.classList.contains("session-shortcuts-visible");
  }

  currentSessionPath() {
    return this.element?.querySelector("a.session.selected[data-session-path]")?.dataset.sessionPath || new URLSearchParams(this.window.location.search).get("session") || "";
  }

  activeSearch() {
    return new URLSearchParams(this.window.location.search).get("session_search") || "";
  }

  modalIsOpen() {
    return !!this.document.querySelector("[data-modal]:not([hidden])");
  }

  current(epoch, element) {
    return epoch === this.asyncEpoch && element === this.element && !!this.element;
  }

  normalLeftClick(event) {
    return event.button === 0 && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey;
  }
}
