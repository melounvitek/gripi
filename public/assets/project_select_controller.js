export class ProjectSelectController {
  constructor(document, window) {
    this.document = document;
    this.window = window;
    this.openWrapper = null;
    this.serial = 0;
    this.listenersBound = false;
  }

  initialize(root = this.document) {
    this.bindPageListeners();
    this.wrappers(root).forEach((wrapper) => this.enhance(wrapper));
  }

  destroy(root) {
    this.wrappers(root).forEach((wrapper) => {
      const state = wrapper._projectSelectState;
      if (!state) return;

      this.close(wrapper);
      clearTimeout(state.typeaheadTimer);
      state.listbox.remove();
      state.trigger.remove();
      state.select.classList.remove("project-select-native-hidden");
      state.select.tabIndex = state.originalTabIndex;
      if (state.originalAriaHidden === null) state.select.removeAttribute("aria-hidden");
      else state.select.setAttribute("aria-hidden", state.originalAriaHidden);
      if (state.associatedLabel && state.originalLabelFor !== null) state.associatedLabel.htmlFor = state.originalLabelFor;
      delete wrapper._projectSelectState;
    });
  }

  sync(select) {
    const state = select?.closest("[data-project-select]")?._projectSelectState;
    const selectedOption = select?.selectedOptions[0];
    if (!state || !selectedOption) return;

    this.renderOption(state.trigger, selectedOption, true);
    state.options.forEach((customOption, index) => {
      customOption.setAttribute("aria-selected", index === select.selectedIndex ? "true" : "false");
    });
  }

  close(wrapper = this.openWrapper, { restoreFocus = false } = {}) {
    const state = wrapper?._projectSelectState;
    if (!state) return false;

    state.listbox.hidden = true;
    state.trigger.setAttribute("aria-expanded", "false");
    state.trigger.removeAttribute("aria-activedescendant");
    state.options.forEach((option) => option.classList.remove("is-active"));
    if (this.openWrapper === wrapper) this.openWrapper = null;
    if (restoreFocus) state.trigger.focus();
    return true;
  }

  isActive(root = this.document) {
    const activeWrapper = this.document.activeElement?.closest?.("[data-project-select]");
    if (activeWrapper && this.contains(root, activeWrapper)) return true;
    return this.wrappers(root).some((wrapper) => wrapper._projectSelectState?.trigger.getAttribute("aria-expanded") === "true");
  }

  bindPageListeners() {
    if (this.listenersBound) return;
    this.listenersBound = true;
    this.document.addEventListener("click", () => this.close());
    this.window.addEventListener("resize", () => this.position(this.openWrapper));
    this.window.addEventListener("scroll", () => this.position(this.openWrapper), true);
  }

  wrappers(root) {
    if (!root) return [];
    const wrappers = Array.from(root.querySelectorAll?.("[data-project-select]") || []);
    if (root.matches?.("[data-project-select]")) wrappers.unshift(root);
    return wrappers;
  }

  contains(root, element) {
    return root === this.document || root === element || !!root?.contains?.(element);
  }

  enhance(wrapper) {
    if (wrapper._projectSelectState) return;
    const select = wrapper.querySelector("select");
    if (!select || !select.options.length) return;

    const id = `project-select-${++this.serial}`;
    const labelledBy = select.getAttribute("aria-labelledby");
    const associatedLabel = labelledBy ? this.document.getElementById(labelledBy) : null;
    const accessibleLabel = select.getAttribute("aria-label") || associatedLabel?.textContent || "Choose project";
    const trigger = this.document.createElement("button");
    trigger.type = "button";
    trigger.id = `${id}-trigger`;
    trigger.className = "project-select-trigger";
    trigger.setAttribute("role", "combobox");
    trigger.setAttribute("aria-haspopup", "listbox");
    trigger.setAttribute("aria-expanded", "false");
    trigger.setAttribute("aria-controls", `${id}-listbox`);
    trigger.setAttribute("aria-owns", `${id}-listbox`);
    trigger.setAttribute("aria-label", accessibleLabel.trim());

    const originalLabelFor = associatedLabel?.htmlFor ?? null;
    if (associatedLabel?.htmlFor === select.id) associatedLabel.htmlFor = trigger.id;

    const listbox = this.document.createElement("div");
    listbox.id = `${id}-listbox`;
    listbox.className = "project-select-listbox";
    listbox.setAttribute("role", "listbox");
    listbox.setAttribute("aria-labelledby", trigger.id);
    listbox.hidden = true;

    const nativeOptions = Array.from(select.options);
    const options = nativeOptions.map((nativeOption, index) => {
      const option = this.document.createElement("div");
      option.id = `${id}-option-${index}`;
      option.className = "project-select-option";
      option.setAttribute("role", "option");
      option.setAttribute("aria-selected", index === select.selectedIndex ? "true" : "false");
      if (nativeOption.dataset.projectForeground) option.style.setProperty("--project-identity-fg", nativeOption.dataset.projectForeground);
      this.renderOption(option, nativeOption);
      option.addEventListener("click", (event) => {
        event.stopPropagation();
        this.selectOption(wrapper, option);
      });
      option.addEventListener("mousemove", () => this.setActiveOption(wrapper, index));
      listbox.append(option);
      return option;
    });

    wrapper._projectSelectState = {
      select, trigger, listbox, nativeOptions, options,
      activeIndex: Math.max(0, select.selectedIndex),
      typeahead: "", typeaheadTimer: null,
      originalTabIndex: select.tabIndex,
      originalAriaHidden: select.getAttribute("aria-hidden"),
      associatedLabel, originalLabelFor
    };
    select.classList.add("project-select-native-hidden");
    select.tabIndex = -1;
    select.setAttribute("aria-hidden", "true");
    wrapper.append(trigger);
    this.document.body.append(listbox);
    this.sync(select);

    trigger.addEventListener("click", (event) => {
      event.stopPropagation();
      if (listbox.hidden) this.open(wrapper);
      else this.close(wrapper);
    });
    trigger.addEventListener("keydown", (event) => this.handleKeydown(event, wrapper));
  }

  renderOption(container, option, includeChevron = false) {
    container.replaceChildren();
    if (option.dataset.projectForeground) container.style.setProperty("--project-identity-fg", option.dataset.projectForeground);
    else container.style.removeProperty("--project-identity-fg");
    const icon = this.document.createElement("span");
    if (option.dataset.projectMonogram) {
      icon.className = "project-identity-icon";
      icon.textContent = option.dataset.projectMonogram;
      icon.style.setProperty("--project-identity-bg", option.dataset.projectBackground);
      icon.style.setProperty("--project-identity-fg", option.dataset.projectForeground);
    } else {
      icon.className = "project-select-neutral-icon";
      icon.textContent = option.dataset.projectOptionKind === "new" ? "+" : "•";
    }
    icon.setAttribute("aria-hidden", "true");

    const label = this.document.createElement("span");
    label.className = includeChevron ? "project-select-trigger-label" : "project-select-option-label";
    label.textContent = option.textContent;
    container.append(icon, label);
    if (!includeChevron) return;

    const chevron = this.document.createElement("span");
    chevron.className = "project-select-chevron";
    chevron.setAttribute("aria-hidden", "true");
    chevron.textContent = "▾";
    container.append(chevron);
  }

  position(wrapper) {
    const state = wrapper?._projectSelectState;
    if (!state || state.listbox.hidden || this.window.matchMedia("(max-width: 760px)").matches) return;

    const rect = state.trigger.getBoundingClientRect();
    const padding = 8;
    const gap = 6;
    const below = this.window.innerHeight - rect.bottom - padding - gap;
    const above = rect.top - padding - gap;
    const openBelow = below >= Math.min(240, above) || below >= above;
    const maxHeight = Math.max(120, Math.min(320, openBelow ? below : above));
    const width = Math.min(Math.max(rect.width, 240), this.window.innerWidth - padding * 2);
    const left = Math.min(Math.max(padding, rect.left), this.window.innerWidth - width - padding);
    state.listbox.style.width = `${width}px`;
    state.listbox.style.maxHeight = `${maxHeight}px`;
    state.listbox.style.left = `${left}px`;
    state.listbox.style.top = openBelow ? `${rect.bottom + gap}px` : `${Math.max(padding, rect.top - Math.min(state.listbox.scrollHeight, maxHeight) - gap)}px`;
  }

  open(wrapper) {
    const state = wrapper?._projectSelectState;
    if (!state) return;
    if (this.openWrapper && this.openWrapper !== wrapper) this.close();
    state.listbox.hidden = false;
    state.trigger.setAttribute("aria-expanded", "true");
    this.openWrapper = wrapper;
    this.position(wrapper);
    this.setActiveOption(wrapper, Math.max(0, state.select.selectedIndex));
  }

  setActiveOption(wrapper, index) {
    const state = wrapper?._projectSelectState;
    if (!state || !state.options.length) return;
    state.activeIndex = Math.max(0, Math.min(index, state.options.length - 1));
    state.options.forEach((option, optionIndex) => option.classList.toggle("is-active", optionIndex === state.activeIndex));
    const activeOption = state.options[state.activeIndex];
    state.trigger.setAttribute("aria-activedescendant", activeOption.id);
    activeOption.scrollIntoView({ block: "nearest" });
  }

  selectOption(wrapper, option) {
    const state = wrapper?._projectSelectState;
    const index = state?.options.indexOf(option);
    if (!state || index == null || index < 0) return;
    state.select.selectedIndex = index;
    this.sync(state.select);
    this.close(wrapper, { restoreFocus: true });
    state.select.dispatchEvent(new this.window.Event("change", { bubbles: true }));
  }

  moveActiveOption(wrapper, movement) {
    const state = wrapper?._projectSelectState;
    if (!state) return;
    const lastIndex = state.options.length - 1;
    if (movement === "first") return this.setActiveOption(wrapper, 0);
    if (movement === "last") return this.setActiveOption(wrapper, lastIndex);
    this.setActiveOption(wrapper, (state.activeIndex + movement + state.options.length) % state.options.length);
  }

  handleKeydown(event, wrapper) {
    const state = wrapper._projectSelectState;
    const open = !state.listbox.hidden;
    if (["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) {
      event.preventDefault();
      if (!open) this.open(wrapper);
      if (event.key === "ArrowDown") this.moveActiveOption(wrapper, 1);
      if (event.key === "ArrowUp") this.moveActiveOption(wrapper, -1);
      if (event.key === "Home") this.moveActiveOption(wrapper, "first");
      if (event.key === "End") this.moveActiveOption(wrapper, "last");
      return;
    }
    if (["Enter", " ", "Spacebar"].includes(event.key)) {
      event.preventDefault();
      if (open) this.selectOption(wrapper, state.options[state.activeIndex]);
      else this.open(wrapper);
      return;
    }
    if (event.key === "Escape" && open) {
      event.preventDefault();
      event.stopPropagation();
      this.close(wrapper, { restoreFocus: true });
      return;
    }
    if (event.key === "Tab" && open) return void this.close(wrapper);
    if (event.key.length !== 1 || event.altKey || event.ctrlKey || event.metaKey) return;
    if (!open) this.open(wrapper);

    clearTimeout(state.typeaheadTimer);
    state.typeahead = `${state.typeahead || ""}${event.key}`.toLocaleLowerCase();
    const match = state.nativeOptions.findIndex((option) => option.textContent.trim().toLocaleLowerCase().startsWith(state.typeahead));
    if (match >= 0) this.setActiveOption(wrapper, match);
    state.typeaheadTimer = setTimeout(() => { state.typeahead = ""; }, 600);
  }
}
