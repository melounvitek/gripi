export class NewSessionFormController {
  constructor(document, window, projectSelectController) {
    this.document = document;
    this.window = window;
    this.projectSelectController = projectSelectController;
  }

  initialize(root = this.document) {
    this.forms(root).forEach((form) => {
      if (form._newSessionFormState) return;
      const listeners = {
        input: (event) => this.handleInput(event, form),
        change: (event) => this.handleChange(event, form),
        click: (event) => this.handleClick(event, form),
        focusout: (event) => this.handleFocusout(event, form),
        keydown: (event) => this.handleKeydown(event, form)
      };
      form._newSessionFormState = { timer: null, controller: null, listeners };
      Object.entries(listeners).forEach(([type, listener]) => form.addEventListener(type, listener));
    });
  }

  destroy(root) {
    this.forms(root).forEach((form) => {
      const state = form._newSessionFormState;
      if (!state) return;
      this.cancelValidation(form);
      this.clearSuggestions(form);
      Object.entries(state.listeners).forEach(([type, listener]) => form.removeEventListener(type, listener));
      delete form._newSessionFormState;
    });
  }

  open(form) {
    const pathFields = form?.querySelector("[data-new-session-path-fields]");
    const input = form?.querySelector("[data-new-session-cwd-input]");
    const hiddenCwd = form?.querySelector("[data-new-session-cwd-value]");
    if (!pathFields?.hidden && input?.value.trim() && !hiddenCwd?.value) this.validate(form, { delay: 0 });
  }

  close(form) {
    this.cancelValidation(form);
    return this.closeSuggestions(form);
  }

  sync(form) {
    const select = form?.querySelector("[data-new-session-known-cwd]");
    const hiddenCwd = form?.querySelector("[data-new-session-cwd-value]");
    const option = select?.selectedOptions[0];
    if (option && !option.hasAttribute("data-new-session-new-path-option") && hiddenCwd) hiddenCwd.value = option.value;
  }

  setValidationState(form, state, message) {
    const status = form?.querySelector("[data-new-session-cwd-message]");
    const submit = form?.querySelector("[data-new-session-submit]");
    if (!status || !submit) return;
    status.hidden = false;
    status.textContent = message;
    status.classList.toggle("is-valid", state === "valid");
    status.classList.toggle("is-invalid", state === "invalid");
    submit.disabled = state !== "valid";
  }

  closeSuggestions(form) {
    const list = form?.querySelector("[data-new-session-cwd-suggestions]");
    if (!list || list.hidden) return false;
    this.clearSuggestions(form);
    return true;
  }

  forms(root) {
    if (!root) return [];
    const forms = Array.from(root.querySelectorAll?.(".new-session-cwd-form") || []);
    if (root.matches?.(".new-session-cwd-form")) forms.unshift(root);
    return forms;
  }

  cancelValidation(form) {
    const state = form?._newSessionFormState;
    if (!state) return;
    clearTimeout(state.timer);
    state.timer = null;
    state.controller?.abort();
    state.controller = null;
  }

  clearSuggestions(form) {
    const input = form?.querySelector("[data-new-session-cwd-input]");
    const list = form?.querySelector("[data-new-session-cwd-suggestions]");
    if (!input || !list) return;
    list.replaceChildren();
    list.hidden = true;
    delete list.dataset.activeIndex;
    input.setAttribute("aria-expanded", "false");
    input.removeAttribute("aria-activedescendant");
  }

  renderSuggestions(form, directories) {
    const input = form?.querySelector("[data-new-session-cwd-input]");
    const list = form?.querySelector("[data-new-session-cwd-suggestions]");
    if (!input || !list) return;

    this.clearSuggestions(form);
    directories.forEach((path, index) => {
      const option = this.document.createElement("button");
      option.type = "button";
      option.id = `new-session-cwd-suggestion-${index}`;
      option.className = "cwd-suggestion";
      option.setAttribute("role", "option");
      option.setAttribute("aria-selected", "false");
      option.tabIndex = -1;
      option.dataset.cwdSuggestion = path;
      option.textContent = path;
      list.append(option);
    });
    if (list.children.length === 0) return;
    list.hidden = false;
    input.setAttribute("aria-expanded", "true");
  }

  setActiveSuggestion(form, index) {
    const input = form?.querySelector("[data-new-session-cwd-input]");
    const list = form?.querySelector("[data-new-session-cwd-suggestions]");
    const options = Array.from(list?.querySelectorAll("[data-cwd-suggestion]") || []);
    if (!input || !list || options.length === 0) return;

    const activeIndex = (index + options.length) % options.length;
    list.dataset.activeIndex = String(activeIndex);
    options.forEach((option, optionIndex) => {
      const active = optionIndex === activeIndex;
      option.classList.toggle("is-active", active);
      option.setAttribute("aria-selected", active ? "true" : "false");
    });
    input.setAttribute("aria-activedescendant", options[activeIndex].id);
    options[activeIndex].scrollIntoView({ block: "nearest" });
  }

  selectSuggestion(form, path) {
    const input = form?.querySelector("[data-new-session-cwd-input]");
    if (!input || !path) return;
    input.value = path;
    this.clearSuggestions(form);
    input.focus();
    this.validate(form, { delay: 0 });
  }

  firstProjectOption(select) {
    return Array.from(select?.options || []).find((option) => option.value && !option.hasAttribute("data-new-session-new-path-option"));
  }

  setProjectMode(form) {
    const projectFields = form?.querySelector("[data-new-session-project-fields]");
    const pathFields = form?.querySelector("[data-new-session-path-fields]");
    const select = form?.querySelector("[data-new-session-known-cwd]");
    const hiddenCwd = form?.querySelector("[data-new-session-cwd-value]");
    const status = form?.querySelector("[data-new-session-cwd-message]");
    const submit = form?.querySelector("[data-new-session-submit]");
    if (!form || !projectFields || !pathFields || !select || !hiddenCwd || !submit) return;

    const option = this.firstProjectOption(select);
    if (!option) return this.setPathMode(form, { focus: false });

    this.cancelValidation(form);
    this.clearSuggestions(form);
    projectFields.hidden = false;
    pathFields.hidden = true;
    if (!select.value || select.selectedOptions[0]?.hasAttribute("data-new-session-new-path-option")) select.value = option.value;
    hiddenCwd.value = select.value;
    this.projectSelectController.sync(select);
    submit.disabled = !hiddenCwd.value;
    if (status) status.hidden = true;
  }

  setPathMode(form, { focus = true } = {}) {
    const projectFields = form?.querySelector("[data-new-session-project-fields]");
    const pathFields = form?.querySelector("[data-new-session-path-fields]");
    const input = form?.querySelector("[data-new-session-cwd-input]");
    const hiddenCwd = form?.querySelector("[data-new-session-cwd-value]");
    if (!form || !projectFields || !pathFields || !input || !hiddenCwd) return;

    const startingCwd = hiddenCwd.value;
    projectFields.hidden = true;
    pathFields.hidden = false;
    if (startingCwd) input.value = startingCwd;
    hiddenCwd.value = "";
    this.validate(form, { delay: 0 });
    if (focus) input.focus();
  }

  validate(form, { delay = 250 } = {}) {
    const input = form?.querySelector("[data-new-session-cwd-input]");
    const hiddenCwd = form?.querySelector("[data-new-session-cwd-value]");
    const browserEndpoint = form?.dataset.cwdBrowserUrl;
    if (!input || !hiddenCwd || !browserEndpoint) return;

    this.cancelValidation(form);
    this.clearSuggestions(form);
    hiddenCwd.value = "";
    const cwd = input.value.trim();
    if (!cwd) return this.setValidationState(form, "invalid", "Enter an existing directory.");

    this.setValidationState(form, "pending", "Checking…");
    const state = form._newSessionFormState;
    if (!state) return;
    state.timer = setTimeout(async () => {
      state.timer = null;
      const controller = new AbortController();
      state.controller = controller;
      try {
        const browserUrl = new URL(browserEndpoint, this.window.location.origin);
        browserUrl.searchParams.set("cwd", cwd);
        const response = await fetch(browserUrl, { headers: { "Accept": "application/json" }, signal: controller.signal });
        const payload = await response.json().catch(() => null);
        if (controller.signal.aborted || input.value.trim() !== cwd) return;
        if (!response.ok || !payload) throw new Error("cwd browser failed");

        const directories = Array.isArray(payload.directories) ? payload.directories : [];
        this.renderSuggestions(form, directories);
        if (payload.valid) {
          hiddenCwd.value = payload.cwd || cwd;
          this.setValidationState(form, "valid", "Directory exists.");
        } else if (directories.length > 0) {
          this.setValidationState(form, "pending", "Choose a matching directory.");
        } else {
          this.setValidationState(form, "invalid", payload.error || "Path must be an existing directory.");
        }
      } catch (_error) {
        if (!controller.signal.aborted) this.setValidationState(form, "invalid", "Could not browse this path.");
      } finally {
        if (state.controller === controller) state.controller = null;
      }
    }, delay);
  }

  handleInput(event, form) {
    if (event.target.closest?.("[data-new-session-cwd-input]")) this.validate(form);
  }

  handleChange(event, form) {
    const select = event.target.closest?.("[data-new-session-known-cwd]");
    if (!select) return;
    this.projectSelectController.sync(select);
    if (select.selectedOptions[0]?.hasAttribute("data-new-session-new-path-option")) this.setPathMode(form);
    else this.setProjectMode(form);
  }

  handleClick(event, form) {
    const option = event.target.closest?.("[data-cwd-suggestion]");
    if (option) {
      event.preventDefault();
      this.selectSuggestion(form, option.dataset.cwdSuggestion);
      return;
    }
    const button = event.target.closest?.("[data-new-session-project-mode]");
    if (!button) return;
    event.preventDefault();
    this.setProjectMode(form);
  }

  handleFocusout(event, form) {
    const next = event.relatedTarget;
    if (next?.closest?.("[data-new-session-cwd-input], [data-new-session-cwd-suggestions]")) return;
    this.clearSuggestions(form);
  }

  handleKeydown(event, form) {
    const input = event.target.closest?.("[data-new-session-cwd-input]");
    const list = form.querySelector("[data-new-session-cwd-suggestions]");
    const options = Array.from(list?.querySelectorAll("[data-cwd-suggestion]") || []);
    if (input && list && !list.hidden && options.length > 0) {
      const activeIndex = Number(list.dataset.activeIndex ?? -1);
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        const nextIndex = activeIndex < 0 && event.key === "ArrowUp" ? options.length - 1 : activeIndex + (event.key === "ArrowDown" ? 1 : -1);
        this.setActiveSuggestion(form, nextIndex);
        return;
      }
      if ((event.key === "Enter" || event.key === "Tab") && activeIndex >= 0) {
        event.preventDefault();
        this.selectSuggestion(form, options[activeIndex].dataset.cwdSuggestion);
        return;
      }
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        this.clearSuggestions(form);
        return;
      }
    }

    if (event.key !== "Tab" || event.defaultPrevented) return;
    const select = form.querySelector("[data-new-session-known-cwd]");
    const projectTrigger = select?.closest("[data-project-select]")?.querySelector(".project-select-trigger");
    const controls = [
      projectTrigger || select,
      form.querySelector("[data-new-session-cwd-input]"),
      form.querySelector("[data-new-session-project-mode]"),
      form.querySelector("[data-new-session-submit]")
    ].filter((control) => control && !control.disabled && !control.closest("[hidden]"));
    if (controls.length === 0) return;
    const first = controls[0];
    const last = controls[controls.length - 1];
    if ((!event.shiftKey && this.document.activeElement === last) || (event.shiftKey && this.document.activeElement === first)) {
      event.preventDefault();
      (event.shiftKey ? last : first).focus();
    }
  }
}
