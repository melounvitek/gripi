export class SessionTagsController {
  constructor(document, window, callbacks = {}) {
    this.document = document;
    this.window = window;
    this.callbacks = callbacks;
    this.editors = new Map();
    this.state = null;
  }

  initialize() {
    this.dialog = this.document.querySelector(".tag-picker");
    if (!this.dialog) return;
    this.search = this.dialog.querySelector("[data-tag-search]");
    this.options = this.dialog.querySelector("[data-tag-options]");
    this.document.addEventListener("click", (event) => {
      const filter = event.target.closest?.("[data-tag-filter]");
      const edit = event.target.closest?.("[data-tag-edit]");
      const chooser = event.target.closest?.("[data-tag-chooser]");
      if (filter || edit || chooser) {
        event.preventDefault();
        if (filter) this.filter(filter.dataset.tagFilter, filter);
        else this.open(edit?.dataset.tagEdit || "", edit || chooser);
      }
      if (event.target.closest?.("[data-tag-close]")) this.close();
      if (event.target.closest?.("[data-tag-retry]")) this.retry();
      if (event.target === this.dialog) {
        const bounds = this.dialog.getBoundingClientRect();
        if (event.clientX < bounds.left || event.clientX > bounds.right || event.clientY < bounds.top || event.clientY > bounds.bottom) this.close();
      }
    });
    this.dialog.addEventListener("cancel", (event) => { event.preventDefault(); this.close(); });
    this.document.addEventListener("keydown", (event) => {
      if (!this.dialog.open) return;
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopImmediatePropagation();
        this.close();
      } else if (["ArrowDown", "ArrowUp"].includes(event.key)) {
        const controls = [...this.options.querySelectorAll("button:not(:disabled), input:not(:disabled)")];
        if (!controls.length) return;
        event.preventDefault();
        const index = controls.indexOf(this.document.activeElement);
        controls[(index + (event.key === "ArrowDown" ? 1 : controls.length - 1)) % controls.length].focus();
      }
    }, true);
    this.search.addEventListener("input", () => {
      this.state.query = this.search.value;
      this.renderOptions();
    });
    this.document.addEventListener("gripi:sidebar-tags", () => this.syncHeader());
  }

  open(path, trigger) {
    this.trigger = trigger;
    this.triggerPath = path;
    this.triggerKind = trigger?.matches("[data-session-actions-toggle]") ? "actions" : path ? "edit" : "filter";
    let state = path ? this.editors.get(path) : null;
    if (!state) {
      state = { path, tags: [], available: [], query: "", loading: false, pending: false, version: 0 };
      if (path) this.editors.set(path, state);
    }
    this.state = state;
    this.dialog.querySelector("h2").textContent = path ? "Session tags" : "Filter by tag";
    const context = this.dialog.querySelector("[data-tag-context]");
    const row = [...this.document.querySelectorAll(".session-row")].find((row) => row.dataset.sessionPath === path);
    context.textContent = row?.dataset.sessionName || (this.document.querySelector("[data-tag-session]")?.dataset.tagSession === path ? this.document.querySelector(".session-header-name")?.textContent : "");
    context.hidden = !context.textContent || !path;
    const label = path ? "Find or create a tag" : "Find a tag";
    this.search.setAttribute("aria-label", label);
    this.search.placeholder = `${label}…`;
    this.search.value = state.query;
    this.dialog.querySelector("[data-tag-help]").textContent = path ? "Changes apply immediately. Tags are available across all projects." : "Across all projects";
    this.callbacks.openModal?.(this.dialog);
    this.dialog.hidden = false;
    this.dialog.showModal();
    this.search.focus();
    this.render();
    if (!state.pending && !state.error) this.load(state);
  }

  close() {
    this.dialog.close();
    this.dialog.hidden = true;
    this.callbacks.closeModal?.(this.dialog);
    const header = this.document.querySelector("[data-tag-session]");
    const row = [...this.document.querySelectorAll(".session-row")].find((row) => row.dataset.sessionPath === this.triggerPath);
    const fallback = this.triggerKind === "actions" ? row?.querySelector("[data-session-actions-toggle]") : this.triggerKind === "edit" && header?.dataset.tagSession === this.triggerPath ? header.querySelector("[data-tag-edit]") : this.document.querySelector("[data-tag-chooser]");
    (this.trigger?.isConnected ? this.trigger : fallback)?.focus({ preventScroll: true });
  }

  async load(state) {
    const version = ++state.version;
    state.loading = true;
    state.error = null;
    this.render();
    try {
      const url = state.path ? `/sessions/tags?${new URLSearchParams({ session: state.path })}` : "/tags";
      const payload = await this.request(url);
      if (version !== state.version) return;
      state.available = state.path ? payload.available_tags : payload.tags;
      if (state.path) state.tags = payload.tags;
    } catch (error) {
      if (version === state.version) state.error = error.message;
    } finally {
      if (version === state.version) state.loading = false;
      if (state === this.state) this.render();
    }
  }

  async mutate(state, tag, assigned) {
    if (state.pending) return;
    state.version += 1;
    state.pending = true;
    state.error = null;
    state.retry = { tag, assigned };
    this.callbacks.invalidate?.();
    this.render();
    try {
      const body = new URLSearchParams({ session: state.path, tag, assigned: String(assigned) });
      const payload = await this.request("/sessions/tags", { method: "POST", body });
      state.tags = payload.tags;
      state.available = payload.available_tags;
      state.retry = null;
      this.updateHeader(payload.session, payload.tags);
      this.callbacks.refresh?.().catch(() => {});
    } catch (error) {
      state.error = error.message;
    } finally {
      state.pending = false;
      if (state === this.state) {
        const restoreFocus = this.dialog.open && [this.document.body, this.dialog].includes(this.document.activeElement);
        this.render();
        if (restoreFocus) ([...this.options.querySelectorAll("[data-tag-option]")].find((control) => control.dataset.tagOption === tag) || this.search).focus({ preventScroll: true });
      }
    }
  }

  async request(url, options) {
    const response = await fetch(url, options);
    const text = await response.text();
    let payload;
    try { payload = JSON.parse(text); } catch (_error) {}
    if (!response.ok) throw new Error(payload?.error || text.trim() || "Could not update tags");
    if (!payload) throw new Error("Could not load tags");
    return payload;
  }

  retry() {
    if (this.state.retry) this.mutate(this.state, this.state.retry.tag, this.state.retry.assigned);
    else if (this.state.filterTag !== undefined) this.filter(this.state.filterTag);
    else this.load(this.state);
  }

  async filter(tag, trigger) {
    const state = this.state;
    const wasOpen = this.dialog.open;
    const target = new URL(this.window.location.href);
    if (tag) target.searchParams.set("tag", tag);
    else target.searchParams.delete("tag");
    try {
      const result = await this.callbacks.filter(target);
      if (result && wasOpen && this.dialog.open && state === this.state) this.close();
    } catch (_error) {
      if (state !== this.state || wasOpen !== this.dialog.open) return;
      if (!this.dialog.open) this.open("", trigger);
      this.state.error = "Could not filter sessions. Try again.";
      this.state.filterTag = tag;
      this.render();
    }
  }

  render() {
    const state = this.state;
    if (!state) return;
    const status = this.dialog.querySelector("[data-tag-status]");
    status.textContent = state.loading ? "Loading tags…" : state.pending ? "Saving…" : "";
    status.hidden = !status.textContent;
    const error = this.dialog.querySelector("[data-tag-error]");
    error.textContent = state.error || "";
    error.hidden = !state.error;
    this.dialog.querySelector("[data-tag-retry]").hidden = !state.error;
    this.renderOptions();
  }

  renderOptions() {
    const state = this.state;
    const focusedTag = this.document.activeElement?.dataset.tagOption;
    this.options.replaceChildren();
    if (state.loading) return;
    const query = state.query.trim().toLowerCase();
    if (!state.path) this.addOption("All tags", "", null);
    for (const tag of state.available.filter((tag) => tag.name.includes(query))) {
      this.addOption(tag.name, tag.name, tag.count);
    }
    if (state.path && query && !state.available.some((tag) => tag.name === query)) {
      const button = this.document.createElement("button");
      button.type = "button";
      button.className = "tag-picker-option tag-create";
      button.textContent = `Create “${query}”`;
      button.disabled = state.pending;
      button.addEventListener("click", () => this.mutate(state, query, true));
      this.options.append(button);
    }
    if (!this.options.children.length) this.options.textContent = "No matching tags.";
    if (focusedTag !== undefined) [...this.options.querySelectorAll("[data-tag-option]")].find((control) => control.dataset.tagOption === focusedTag)?.focus({ preventScroll: true });
  }

  addOption(label, tag, count) {
    const state = this.state;
    const option = this.document.createElement(state.path ? "label" : "button");
    option.className = "tag-picker-option";
    if (state.path) {
      const checkbox = this.document.createElement("input");
      checkbox.type = "checkbox";
      checkbox.checked = state.tags.includes(tag);
      checkbox.disabled = state.pending;
      checkbox.dataset.tagOption = tag;
      checkbox.setAttribute("aria-label", tag);
      checkbox.addEventListener("change", () => {
        const assigned = checkbox.checked;
        checkbox.checked = state.tags.includes(tag);
        this.mutate(state, tag, assigned);
      });
      option.append(checkbox);
    } else {
      option.type = "button";
      option.dataset.tagOption = tag;
      option.setAttribute("aria-pressed", String(tag === (new URL(this.window.location.href).searchParams.get("tag") || "")));
      option.addEventListener("click", () => this.filter(tag));
    }
    const text = this.document.createElement("span");
    text.textContent = label;
    option.append(text);
    if (count !== null) {
      const total = this.document.createElement("span");
      total.className = "tag-count";
      total.textContent = String(count);
      option.append(total);
    }
    this.options.append(option);
  }

  syncHeader() {
    const header = this.document.querySelector("[data-tag-session]");
    const row = [...this.document.querySelectorAll(".session-row")].find((row) => row.dataset.sessionPath === header?.dataset.tagSession);
    if (row) this.updateHeader(row.dataset.sessionPath, JSON.parse(row.dataset.sessionTags || "[]") || []);
  }

  updateHeader(path, tags) {
    const header = this.document.querySelector("[data-tag-session]");
    if (!header || header.dataset.tagSession !== path) return;
    const chips = [...header.querySelectorAll("[data-tag-filter]")];
    if (chips.length === tags.length && chips.every((chip, index) => chip.dataset.tagFilter === tags[index])) return;
    const edit = header.querySelector("[data-tag-edit]");
    const focusedTag = chips.includes(this.document.activeElement) ? this.document.activeElement.dataset.tagFilter : null;
    chips.forEach((chip) => chip.remove());
    for (const tag of tags) {
      const chip = this.document.createElement("button");
      chip.type = "button";
      chip.className = "tag-chip";
      chip.dataset.tagFilter = tag;
      chip.setAttribute("aria-label", `Filter sessions by ${tag}`);
      const label = this.document.createElement("span");
      label.textContent = tag;
      chip.append(label);
      header.insertBefore(chip, edit);
    }
    edit.textContent = tags.length ? "Edit tags" : "Add tags";
    if (focusedTag !== null) ([...header.querySelectorAll("[data-tag-filter]")].find((chip) => chip.dataset.tagFilter === focusedTag) || edit).focus({ preventScroll: true });
  }
}
