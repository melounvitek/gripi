export class SessionActionsController {
  constructor(document, window, callbacks = {}) {
    this.document = document;
    this.window = window;
    this.callbacks = callbacks;
    this.target = null;
    this.pinOperationActive = false;
    this.operationAbort = null;
    this.initialized = false;
  }

  initialize() {
    if (this.initialized) return;
    this.initialized = true;
    this.document.addEventListener("click", (event) => this.handleClick(event));
    this.document.addEventListener("contextmenu", (event) => this.handleContextMenu(event));
    this.document.addEventListener("keydown", (event) => this.handleKeydown(event), true);
    this.document.addEventListener("submit", (event) => this.handleSubmit(event));
  }

  handleClick(event) {
    const toggle = event.target.closest?.("[data-session-actions-toggle]");
    if (toggle) {
      event.preventDefault();
      event.stopPropagation?.();
      const rect = toggle.getBoundingClientRect();
      this.openMenu(toggle.closest(".session-row"), { x: rect.left, y: rect.bottom });
      return;
    }

    const action = event.target.closest?.("[data-session-action]");
    if (action && this.menu()?.contains(action)) {
      event.preventDefault();
      this.performAction(action.dataset.sessionAction);
      return;
    }
    if (!event.target.closest?.("[data-session-actions-menu]")) this.closeMenu();
  }

  handleContextMenu(event) {
    const row = event.target.closest?.(".session-row");
    if (!row) return;
    event.preventDefault();
    this.openMenu(row, { x: event.clientX, y: event.clientY });
  }

  handleKeydown(event) {
    const menu = this.menu();
    if (event.key === "Escape" && !menu?.hidden) {
      event.preventDefault();
      event.stopImmediatePropagation?.();
      this.closeMenu({ restoreFocus: true });
      return;
    }
    if (!menu?.hidden && ["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) {
      event.preventDefault();
      this.moveMenuFocus(event.key);
      return;
    }
    this.openKeyboardMenu(event);
  }

  openKeyboardMenu(event) {
    if (event.key !== "ContextMenu" && !(event.shiftKey && event.key === "F10")) return;
    const row = event.target.closest?.(".session-row");
    if (!row) return;
    event.preventDefault();
    const rect = row.getBoundingClientRect();
    this.openMenu(row, { x: rect.left, y: rect.bottom });
  }

  moveMenuFocus(key) {
    const items = Array.from(this.menu()?.querySelectorAll("button") || []);
    if (items.length === 0) return;
    const current = items.indexOf(this.document.activeElement);
    let index = key === "End" ? items.length - 1 : 0;
    if (key === "ArrowDown") index = (current + 1) % items.length;
    if (key === "ArrowUp") index = (current - 1 + items.length) % items.length;
    items[index].focus();
  }

  openMenu(row, position) {
    const menu = this.menu();
    if (!row || !menu) return false;
    this.closeMenu();
    this.target = this.targetFor(row);
    const pin = menu.querySelector("[data-session-action-pin]");
    if (pin) pin.textContent = this.target.pinned ? "Unpin" : "Pin";
    this.configureDeleteAction(menu.querySelector("[data-session-action-delete]"));
    row.querySelector("[data-session-actions-toggle]")?.setAttribute("aria-expanded", "true");
    menu.hidden = false;
    this.positionMenu(menu, position);
    menu.querySelector("button")?.focus();
    return true;
  }

  targetFor(row) {
    return {
      row,
      path: row.dataset.sessionPath,
      name: row.dataset.sessionName,
      current: row.dataset.current === "true",
      busy: row.dataset.busy === "true",
      pinned: row.dataset.pinned === "true"
    };
  }

  configureDeleteAction(button) {
    if (!button) return;
    const reason = this.target.current ? "Cannot delete the current session" : this.target.busy ? "Cannot delete a running session" : "";
    button.setAttribute("aria-disabled", reason ? "true" : "false");
    button.title = reason;
  }

  positionMenu(menu, { x = 0, y = 0 } = {}) {
    const bounds = menu.getBoundingClientRect();
    const width = bounds.width || 180;
    const height = bounds.height || Math.max(120, (bounds.bottom || 0) - (bounds.top || 0));
    menu.style.left = `${Math.max(8, Math.min(x, this.window.innerWidth - width - 8))}px`;
    menu.style.top = `${Math.max(8, Math.min(y, this.window.innerHeight - height - 8))}px`;
  }

  closeMenu({ restoreFocus = false } = {}) {
    const menu = this.menu();
    if (menu) menu.hidden = true;
    this.target?.row.querySelector("[data-session-actions-toggle]")?.setAttribute("aria-expanded", "false");
    if (restoreFocus) this.restoreFocus();
  }

  restoreFocus() {
    const path = this.target?.path;
    const row = Array.from(this.document.querySelectorAll(".session-row")).find((candidate) => candidate.dataset.sessionPath === path);
    (row || this.target?.row)?.querySelector("[data-session-actions-toggle]")?.focus({ preventScroll: true });
  }

  performAction(action) {
    const target = this.target;
    if (!target || action === "delete" && (target.current || target.busy)) return;
    this.closeMenu();
    if (action === "rename") this.openRename(target);
    if (action === "pin") this.togglePin(target).catch(() => {});
    if (action === "delete") this.openDelete(target);
  }

  openRename(target) {
    const modal = this.document.querySelector('[data-modal="session-rename-modal"]');
    const form = modal?.querySelector("[data-session-rename-form]");
    if (!modal || !form) return;
    this.prepareForm(form);
    form.querySelector('[name="session"]').value = target.path;
    form.querySelector('[name="name"]').value = target.name;
    this.callbacks.openModal?.(modal);
    form.querySelector('[name="name"]').select?.();
  }

  openDelete(target) {
    const modal = this.document.querySelector('[data-modal="session-delete-modal"]');
    const form = modal?.querySelector("[data-session-delete-form]");
    if (!modal || !form) return;
    this.prepareForm(form);
    form.querySelector('[name="session"]').value = target.path;
    form.querySelector('[name="current_session"]').value = this.callbacks.currentSessionPath?.() || "";
    modal.querySelector("[data-session-delete-name]").textContent = target.name;
    this.callbacks.openModal?.(modal);
  }

  async togglePin(target) {
    if (this.pinOperationActive) return null;
    this.pinOperationActive = true;
    try {
      const body = new URLSearchParams({ session: target.path, pinned: target.pinned ? "false" : "true" });
      const response = await fetch("/sessions/pin", { method: "POST", body, headers: { "Accept": "application/json" } });
      if (!response.ok) throw new Error("Could not update pinned session");
      const payload = await response.json();
      await this.callbacks.refresh?.();
      return payload;
    } catch (error) {
      this.callbacks.showError?.(error.message);
      throw error;
    } finally {
      this.pinOperationActive = false;
    }
  }

  handleSubmit(event) {
    const rename = event.target.closest?.("[data-session-rename-form]");
    const remove = event.target.closest?.("[data-session-delete-form]");
    const form = rename || remove;
    if (!form) return;
    event.preventDefault();
    this.submit(form, remove ? "delete" : "rename");
  }

  async submit(form, action) {
    if (action === "delete") form.querySelector('[name="current_session"]').value = this.callbacks.currentSessionPath?.() || "";
    const body = new FormData(form);
    const controls = Array.from(form.querySelectorAll("button, input"));
    controls.forEach((control) => { control.disabled = true; });
    this.clearError(form);
    const abort = new AbortController();
    this.operationAbort?.abort();
    this.operationAbort = abort;
    await this.sendMutation(form, action, body, abort);
    if (this.operationAbort !== abort) return;
    this.operationAbort = null;
    controls.forEach((control) => { control.disabled = false; });
  }

  async sendMutation(form, action, body, abort) {
    try {
      const response = await fetch(form.action, { method: "POST", body, headers: { "Accept": "application/json" }, signal: abort.signal });
      const responseText = await response.text();
      let payload = null;
      try { payload = JSON.parse(responseText); } catch (_error) {}
      if (!response.ok) throw new Error(payload?.error || responseText.trim() || `Could not ${action} session`);
      this.callbacks.closeModal?.(form.closest("[data-modal]"));
      await this.callbacks.refresh?.();
      this.callbacks.showStatus?.(action === "delete" ? "Session deleted" : "Session renamed");
    } catch (error) {
      if (error.name !== "AbortError") this.showError(form, error.message);
    }
  }

  prepareForm(form) {
    this.operationAbort?.abort();
    this.operationAbort = null;
    form.querySelectorAll("button, input").forEach((control) => { control.disabled = false; });
    this.clearError(form);
  }

  modalClosed(modal) {
    if (!["session-rename-modal", "session-delete-modal"].includes(modal?.dataset.modal)) return;
    this.operationAbort?.abort();
    this.operationAbort = null;
  }

  clearError(form) {
    const error = form?.querySelector("[data-session-action-error]");
    if (!error) return;
    error.hidden = true;
    error.textContent = "";
  }

  showError(form, message) {
    const error = form?.querySelector("[data-session-action-error]");
    if (!error) return;
    error.textContent = message;
    error.hidden = false;
  }

  menu() {
    return this.document.querySelector("[data-session-actions-menu]");
  }
}
