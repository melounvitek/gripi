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
      const draft = event.target.closest?.("[data-tag-draft-add]");
      const remove = event.target.closest?.("[data-tag-draft-remove]");
      if (remove) {
        const form = remove.closest("form");
        this.renderDraft(form, [...new FormData(form).getAll("tags")].filter((tag) => tag !== remove.dataset.tagDraftRemove));
        form.querySelector("[data-tag-draft-add]").focus();
      }
      if (filter || edit || chooser || draft) {
        event.preventDefault();
        if (filter) this.filter(filter.dataset.tagFilter, filter);
        else this.open(edit?.dataset.tagEdit || "", edit || chooser || draft, draft?.closest("form"));
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
        if (index < 0) controls[event.key === "ArrowDown" ? 0 : controls.length - 1].focus();
        else controls[(index + (event.key === "ArrowDown" ? 1 : controls.length - 1)) % controls.length].focus();
      }
    }, true);
    this.search.addEventListener("input", () => {
      this.state.query = this.search.value;
      this.renderOptions();
    });
    this.document.addEventListener("gripi:sidebar-tags", () => this.syncHeader());
    this.window.addEventListener("resize", () => { if (this.dialog.open) this.position(); });
  }

  resetDraft(form) {
    const field = form?.querySelector("[data-new-session-tags]");
    if (!field) return;
    this.renderDraft(form, field.dataset.prefillTag ? [field.dataset.prefillTag] : []);
  }

  renderDraft(form, tags) {
    const chips = form.querySelector("[data-tag-draft-chips]");
    chips.replaceChildren();
    for (const tag of tags) {
      const input = this.document.createElement("input");
      input.type = "hidden";
      input.name = "tags";
      input.value = tag;
      const chip = this.document.createElement("button");
      chip.type = "button";
      chip.className = "tag-chip";
      chip.dataset.tagDraftRemove = tag;
      chip.setAttribute("aria-label", `Remove ${tag}`);
      const label = this.document.createElement("span");
      label.textContent = tag;
      chip.append(label, " ×");
      chips.append(input, chip);
    }
    const field = form.querySelector("[data-new-session-tags]");
    field.querySelector("[data-tag-draft-help]").textContent = field.dataset.prefillTag && tags.includes(field.dataset.prefillTag)
      ? "Added from your current filter. Remove it if this session is unrelated."
      : "Optional. Group related sessions across projects.";
  }

  open(path, trigger, form = null) {
    this.trigger = trigger;
    this.anchor = trigger?.getBoundingClientRect();
    this.triggerPath = path;
    this.triggerKind = trigger?.matches("[data-session-actions-toggle]") ? "actions" : path ? "edit" : "filter";
    let state = path ? this.editors.get(path) : null;
    if (!state) {
      state = { path, form, tags: form ? new FormData(form).getAll("tags") : [], available: [], query: "", loading: false, pending: false, version: 0 };
      if (path) this.editors.set(path, state);
    }
    this.state = state;
    this.dialog.querySelector("h2").textContent = form ? "New session tags" : path ? "Session tags" : "Filter by tag";
    const context = this.dialog.querySelector("[data-tag-context]");
    const row = [...this.document.querySelectorAll(".session-row")].find((row) => row.dataset.sessionPath === path);
    context.textContent = row?.dataset.sessionName || (this.document.querySelector("[data-tag-session]")?.dataset.tagSession === path ? this.document.querySelector(".session-header-name")?.textContent : "");
    context.hidden = !context.textContent || !path;
    const label = path || form ? "Find or create a tag" : "Find a tag";
    this.search.setAttribute("aria-label", label);
    this.search.placeholder = `${label}…`;
    this.search.value = state.query;
    this.dialog.querySelector("[data-tag-help]").textContent = form ? "Tags are saved when you start the session." : path ? "Changes apply immediately. Tags are available across all projects." : "Across all projects";
    this.dialog.hidden = false;
    this.dialog.showModal();
    this.callbacks.openModal?.(this.dialog);
    this.search.focus();
    this.render();
    if (!state.pending && !state.error) this.load(state);
  }

  position() {
    if (this.window.matchMedia("(max-width: 760px)").matches) return;
    const anchor = this.trigger?.isConnected ? this.trigger.getBoundingClientRect() : this.anchor;
    if (!anchor) return;
    const { width, height } = this.dialog.getBoundingClientRect();
    const left = Math.max(12, Math.min(anchor.left, this.window.innerWidth - width - 12));
    const below = anchor.bottom + 6;
    const top = below + height <= this.window.innerHeight - 12 ? below : Math.max(12, anchor.top - height - 6);
    this.dialog.style.setProperty("--tag-picker-left", `${left}px`);
    this.dialog.style.setProperty("--tag-picker-top", `${top}px`);
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
    if (state.form) {
      state.tags = assigned ? [...new Set([...state.tags, tag])] : state.tags.filter((name) => name !== tag);
      this.renderDraft(state.form, state.tags);
      this.render();
      ([...this.options.querySelectorAll("[data-tag-option]")].find((control) => control.dataset.tagOption === tag) || this.search).focus({ preventScroll: true });
      return;
    }
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
    const editable = state.path || state.form;
    const available = [...state.available];
    if (state.form) {
      for (const name of state.tags) {
        if (!available.some((tag) => tag.name === name)) available.push({ name, count: null });
      }
    }
    if (!editable) this.addOption("All tags", "", null);
    for (const tag of available.filter((tag) => tag.name.includes(query))) {
      this.addOption(tag.name, tag.name, editable ? null : tag.count);
    }
    if (editable && query && !available.some((tag) => tag.name === query)) {
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
    this.position();
  }

  addOption(label, tag, count) {
    const state = this.state;
    const option = this.document.createElement(state.path || state.form ? "label" : "button");
    option.className = "tag-picker-option";
    if (state.path || state.form) {
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
