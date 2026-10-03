import { recentSessionShortcutFromEvent } from "./shortcuts.js";

const RECENT_PROJECTS = 5;

export class NewSessionFormController {
  constructor(document, window) {
    this.document = document;
    this.window = window;
  }

  initialize(root = this.document) {
    this.forms(root).forEach((form) => {
      if (form._newSessionFormState) return;
      const listeners = {
        input: () => this.refresh(form),
        click: (event) => this.handleClick(event, form),
        keydown: (event) => this.handleKeydown(event, form)
      };
      form._newSessionFormState = { timer: null, controller: null, listeners, browsed: null, cursor: 0, expanded: false };
      Object.entries(listeners).forEach(([type, listener]) => form.addEventListener(type, listener));
    });
  }

  destroy(root) {
    this.forms(root).forEach((form) => {
      const state = form._newSessionFormState;
      if (!state) return;
      this.cancelBrowse(form);
      Object.entries(state.listeners).forEach(([type, listener]) => form.removeEventListener(type, listener));
      delete form._newSessionFormState;
    });
  }

  open(form) {
    if (!form?._newSessionFormState) return;
    form._newSessionFormState.expanded = false;
    this.enter(form, this.projects(form).length ? "" : this.newPath(form));
  }

  close(form) {
    this.cancelBrowse(form);
  }

  setStatus(form, message, invalid = false) {
    const status = form?.querySelector("[data-new-session-status]");
    if (!status) return;
    status.textContent = message;
    status.hidden = !message;
    status.classList.toggle("is-invalid", invalid);
  }

  forms(root) {
    if (!root) return [];
    const forms = Array.from(root.querySelectorAll?.(".new-session-cwd-form") || []);
    if (root.matches?.(".new-session-cwd-form")) forms.unshift(root);
    return forms;
  }

  input(form) {
    return form.querySelector("[data-new-session-input]");
  }

  projects(form) {
    return [...form.querySelectorAll("[data-new-session-project]")];
  }

  options(form) {
    return [...form.querySelectorAll('[role="option"]')].filter((option) => !option.hidden);
  }

  // A value starting like a path browses directories; anything else filters the projects.
  pathMode(form) {
    return /^[/~]/.test(this.input(form).value.trim());
  }

  displayPath(form, path) {
    const home = form.dataset.home;
    return home && (path === home || path.startsWith(`${home}/`)) ? `~${path.slice(home.length)}` : path;
  }

  directoryInput(form, path) {
    return `${this.displayPath(form, path).replace(/\/$/, "")}/`;
  }

  // New projects usually sit beside existing ones, so browsing starts in the first project's parent.
  newPath(form) {
    const project = this.projects(form)[0]?.dataset.newSessionProject;
    return this.directoryInput(form, project ? project.slice(0, project.lastIndexOf("/")) : form.dataset.home || "");
  }

  cancelBrowse(form) {
    const state = form?._newSessionFormState;
    if (!state) return;
    clearTimeout(state.timer);
    state.timer = null;
    state.controller?.abort();
    state.controller = null;
  }

  focusInput(form) {
    // On touch screens focusing the input would open the keyboard over the list.
    if (this.window.matchMedia?.("(pointer: fine)").matches !== false) this.input(form).focus();
  }

  enter(form, value) {
    this.input(form).value = value;
    this.focusInput(form);
    this.refresh(form, 0);
  }

  refresh(form, delay = 250) {
    const state = form._newSessionFormState;
    this.cancelBrowse(form);
    state.browsed = null;
    state.cursor = 0;
    if (this.pathMode(form)) {
      // Nothing is selected until the path is checked, so Enter cannot act on a stale directory.
      state.cursor = -1;
      this.browse(form, delay);
    }
    this.render(form);
  }

  browse(form, delay) {
    const state = form._newSessionFormState;
    const cwd = this.input(form).value.trim();
    state.timer = setTimeout(async () => {
      state.timer = null;
      const controller = new AbortController();
      state.controller = controller;
      let browsed = { cwd: "", directories: [], error: "Could not browse this path." };
      try {
        const url = new URL(form.dataset.cwdBrowserUrl, this.window.location.origin);
        url.searchParams.set("cwd", cwd);
        const response = await fetch(url, { headers: { "Accept": "application/json" }, signal: controller.signal });
        const payload = await response.json().catch(() => null);
        if (response.ok && payload) {
          browsed = { cwd: payload.valid ? payload.cwd : "", directories: Array.isArray(payload.directories) ? payload.directories : [], error: payload.error || "Path must be an existing directory." };
        }
      } catch (_error) {}
      if (controller.signal.aborted) return;
      state.controller = null;
      state.browsed = browsed;
      state.cursor = browsed.cwd || browsed.directories.length ? 0 : -1;
      this.render(form);
    }, delay);
  }

  row(action, glyph, label, path = "") {
    const row = this.document.createElement("button");
    row.type = "button";
    row.className = "picker-row new-session-row";
    row.tabIndex = -1;
    row.setAttribute("role", "option");
    row.dataset.newSessionAction = action;
    row.dataset.newSessionPath = path;
    for (const [className, text] of [["picker-cursor", "→"], ["project-monogram", glyph]]) {
      const mark = this.document.createElement("span");
      mark.className = className;
      mark.setAttribute("aria-hidden", "true");
      mark.textContent = text;
      row.append(mark);
    }
    const name = this.document.createElement("span");
    name.className = "new-session-name";
    name.textContent = label;
    row.append(name);
    return row;
  }

  render(form) {
    const state = form._newSessionFormState;
    const list = form.querySelector("[data-new-session-list]");
    const query = this.input(form).value.trim().toLowerCase();
    const path = this.pathMode(form);
    const projects = this.projects(form);
    list.querySelectorAll("[data-new-session-action]").forEach((row) => row.remove());
    const matching = path ? [] : projects.filter((row) => row.querySelector(".new-session-name").textContent.toLowerCase().includes(query));
    const visible = query || state.expanded ? matching : matching.slice(0, RECENT_PROJECTS);
    projects.forEach((row) => { row.hidden = !visible.includes(row); });
    visible.forEach((row, index) => { row.querySelector(".new-session-key").textContent = index < 9 ? index + 1 : ""; });
    if (path) {
      if (state.browsed?.cwd) list.append(this.row("start", "✓", `Start in ${this.displayPath(form, state.browsed.cwd)}`, state.browsed.cwd));
      (state.browsed?.directories || []).forEach((directory) => {
        list.append(this.row("open", "", `${directory.slice(directory.lastIndexOf("/") + 1)}/`, directory));
      });
      if (projects.length) list.append(this.row("back", "←", "Back to projects"));
    } else {
      if (visible.length < matching.length) list.append(this.row("more", "…", `${matching.length - visible.length} more projects`));
      list.append(this.row("path", "+", "Add new path…"));
    }
    this.options(form).forEach((option, index) => { option.id = `new-session-option-${index}`; });
    this.setCursor(form, state.cursor);

    const digits = visible.length > 1 ? `ctrl+1…${Math.min(visible.length, 9)}` : "ctrl+1";
    form.querySelector("[data-new-session-hint]").textContent = path ? "↑↓ navigate · enter open or start · esc cancel" : `↑↓ navigate · enter start${visible.length ? ` · ${digits} start directly` : ""} · esc cancel`;
    if (!path) this.setStatus(form, query && !visible.length ? "No matching projects." : "");
    else if (!state.browsed) this.setStatus(form, "Checking…");
    else this.setStatus(form, state.browsed.cwd || state.browsed.directories.length ? "" : state.browsed.error, true);
  }

  setCursor(form, index) {
    const input = this.input(form);
    const options = this.options(form);
    form._newSessionFormState.cursor = index;
    options.forEach((option, optionIndex) => option.setAttribute("aria-selected", String(optionIndex === index)));
    if (!options[index]) return input.removeAttribute("aria-activedescendant");
    input.setAttribute("aria-activedescendant", options[index].id);
    options[index].scrollIntoView({ block: "nearest" });
  }

  activate(form, option) {
    const action = option.dataset.newSessionAction;
    const cwd = option.dataset.newSessionProject || (action === "start" && option.dataset.newSessionPath);
    if (cwd) {
      form.querySelector("[data-new-session-cwd-value]").value = cwd;
      form.requestSubmit();
    } else if (action === "more") {
      Object.assign(form._newSessionFormState, { expanded: true, cursor: RECENT_PROJECTS });
      this.focusInput(form);
      this.render(form);
    } else if (action === "path") {
      this.enter(form, this.newPath(form));
    } else if (action === "open") {
      this.enter(form, this.directoryInput(form, option.dataset.newSessionPath));
    } else if (action === "back") {
      this.enter(form, "");
    }
  }

  handleClick(event, form) {
    const option = event.target.closest?.('[role="option"]');
    if (option) this.activate(form, option);
  }

  handleKeydown(event, form) {
    const options = this.options(form);
    const cursor = form._newSessionFormState.cursor;
    const shortcut = event.ctrlKey && !event.altKey && !event.metaKey ? recentSessionShortcutFromEvent(event) : null;
    if (shortcut) {
      event.preventDefault();
      const project = this.projects(form).filter((row) => !row.hidden)[shortcut - 1];
      if (project && !event.repeat) this.activate(form, project);
    } else if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      if (!options.length) return;
      const step = event.key === "ArrowDown" ? 1 : -1;
      this.setCursor(form, cursor < 0 ? (step > 0 ? 0 : options.length - 1) : (cursor + step + options.length) % options.length);
    } else if (event.key === "Enter" && event.target === this.input(form) && !event.isComposing) {
      event.preventDefault();
      if (options[cursor]) this.activate(form, options[cursor]);
    }
  }
}
