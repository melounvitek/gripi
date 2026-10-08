import { pointPickerCursor } from "./dom.js";
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
        input: (event) => this.handleInput(event, form),
        click: (event) => {
          const option = event.target.closest?.('[role="option"]');
          if (!option) return;
          if (event.target.closest("[data-new-session-inside]")) this.browse(form, option.dataset.newSessionPath);
          else this.activate(form, option);
        },
        keydown: (event) => this.handleKeydown(event, form)
      };
      // folder is null while the projects are listed; listing is null until that folder's subfolders arrive.
      form._newSessionFormState = { controller: null, listeners, folder: null, listing: null, cursor: 0, expanded: false };
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

  // fork is the { path, name } of a session whose folder is gone, which Pi forks into the folder picked here.
  open(form, fork = null) {
    if (!form?._newSessionFormState) return;
    Object.assign(form._newSessionFormState, { expanded: false, fork });
    form.querySelector("[data-new-session-fork-from]").value = fork?.path || "";
    form.closest("[data-modal]").querySelector("#new-session-modal-title").textContent = fork ? "Continue in another folder" : "New session";
    const note = form.querySelector("[data-new-session-fork-note]");
    note.textContent = fork ? `Pi forks “${fork.name}” into the folder you pick. The original stays as it is.` : "";
    note.hidden = !fork;
    if (this.projects(form).length) this.showProjects(form);
    else this.browse(form, form.dataset.home);
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

  displayPath(form, path) {
    const home = form.dataset.home;
    return home && (path === home || path.startsWith(`${home}/`)) ? `~${path.slice(home.length)}` : path;
  }

  parent(path) {
    return path.replace(/\/+$/, "").replace(/\/[^/]*$/, "") || "/";
  }

  cancelBrowse(form) {
    form?._newSessionFormState?.controller?.abort();
  }

  focusInput(form) {
    // On touch screens focusing the input would open the keyboard over the list.
    if (this.window.matchMedia?.("(pointer: fine)").matches !== false) this.input(form).focus();
  }

  showProjects(form) {
    this.cancelBrowse(form);
    form._newSessionFormState.folder = null;
    this.input(form).value = "";
    this.focusInput(form);
    this.render(form);
  }

  async browse(form, folder, filter = "") {
    const state = form._newSessionFormState;
    this.cancelBrowse(form);
    const controller = new AbortController();
    Object.assign(state, { controller, folder, listing: null });
    this.input(form).value = filter;
    this.focusInput(form);
    this.render(form);
    let payload = null;
    try {
      const url = new URL(form.dataset.cwdBrowserUrl, this.window.location.origin);
      url.searchParams.set("cwd", folder);
      const response = await fetch(url, { headers: { "Accept": "application/json" }, signal: controller.signal });
      if (response.ok) payload = await response.json().catch(() => null);
    } catch (_error) {}
    if (controller.signal.aborted) return;
    if (payload?.valid) Object.assign(state, { folder: payload.cwd, listing: { directories: payload.directories } });
    else state.listing = { error: payload?.error || "Could not list this folder." };
    this.render(form);
  }

  handleInput(event, form) {
    const state = form._newSessionFormState;
    const input = this.input(form);
    // A pasted path means that folder, not what is inside it, so its trailing slash is dropped.
    if (event.inputType === "insertFromPaste") input.value = input.value.replace(/([^~/])\/+$/, "$1");
    const value = input.value;
    const absolute = /^(\/|~\/)/.test(value);
    if (!absolute && (state.folder === null || !value.includes("/"))) return this.render(form);
    // A typed path names the folder to list up to its last slash; the rest filters that folder.
    const cut = value.lastIndexOf("/") + 1;
    this.browse(form, absolute ? value.slice(0, cut) : `${state.folder}/${value.slice(0, cut)}`, value.slice(cut));
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

  render(form, cursor) {
    const state = form._newSessionFormState;
    const list = form.querySelector("[data-new-session-list]");
    const label = form.querySelector("[data-new-session-label]");
    const query = this.input(form).value.trim().toLowerCase();
    const browsing = state.folder !== null;
    const projects = this.projects(form);
    list.querySelectorAll("[data-new-session-action]").forEach((row) => row.remove());
    // The path is searched too: a worktree is often named after its branch, not its project.
    const matching = browsing ? [] : projects.filter((row) => [".new-session-name", ".new-session-path"].some((part) => row.querySelector(part).textContent.toLowerCase().includes(query)));
    const visible = query || state.expanded ? matching : matching.slice(0, RECENT_PROJECTS);
    projects.forEach((row) => { row.hidden = !visible.includes(row); });
    visible.forEach((row, index) => { row.querySelector(".new-session-key").textContent = index < 9 ? index + 1 : ""; });
    let selected = 0;
    const start = state.fork ? "continue" : "start";
    if (browsing) {
      const where = this.displayPath(form, state.folder).replace(/(.)\/+$/, "$1");
      const name = (path) => path.slice(path.lastIndexOf("/") + 1);
      // Hidden folders are listed once the filter starts with a dot.
      const folders = (state.listing?.directories || []).filter((path) => name(path).toLowerCase().includes(query) && (!name(path).startsWith(".") || query.startsWith(".")));
      folders.forEach((path) => {
        const row = this.row("folder", "", name(path), path);
        // A folder that is already a project keeps its letters and last activity.
        const project = projects.find((candidate) => candidate.dataset.newSessionProject === path);
        if (project) {
          row.querySelector(".project-monogram").replaceWith(project.querySelector(".project-monogram").cloneNode(true));
          row.append(project.querySelector(".new-session-age").cloneNode(true));
        }
        // The row starts a session; this mark is a second target that lists the folder's own subfolders.
        const inside = this.document.createElement("span");
        inside.className = "new-session-inside";
        inside.dataset.newSessionInside = "";
        inside.setAttribute("aria-hidden", "true");
        inside.textContent = "›";
        row.append(inside);
        list.append(row);
      });
      // Only a folder row is ever preselected, so Enter never starts in the listed folder itself by accident.
      selected = folders.length ? Math.max(0, folders.findIndex((path) => name(path).toLowerCase() === query)) : -1;
      if (state.listing && !state.listing.error) list.append(this.row("start", "·", `${state.fork ? "Continue" : "Start"} in ${where} itself`, state.folder));
      const up = this.parent(state.folder);
      if (up !== state.folder) list.append(this.row("up", "↑", `Up to ${this.displayPath(form, up)}`, up));
      if (projects.length) list.append(this.row("back", "←", "Back to projects"));

      const place = this.document.createElement("span");
      place.className = "new-session-where";
      place.textContent = where;
      label.replaceChildren("Folders in ", place, ":");
      if (!state.listing) this.setStatus(form, "Loading…");
      else if (state.listing.error) this.setStatus(form, state.listing.error, true);
      else this.setStatus(form, folders.length ? "" : query ? "No matching folders." : "No folders inside.");
    } else {
      const more = matching.length - visible.length;
      if (more) list.append(this.row("more", "…", `${more} more project${more === 1 ? "" : "s"}`));
      list.append(this.row("path", "+", "Other folder…"));

      label.textContent = state.fork ? "Folder:" : "Project or path:";
      this.setStatus(form, query && !visible.length ? "No matching projects." : "");
    }
    list.querySelectorAll('[role="option"]').forEach((option, index) => { option.id = `new-session-option-${index}`; });
    this.setCursor(form, cursor ?? selected);
    form.querySelector("[data-new-session-hint]").textContent = browsing ? `↑↓ navigate · enter ${start} · tab look inside · backspace up · esc cancel` : `↑↓ navigate · enter ${start} · ctrl+1…9 ${start} directly · esc cancel`;
  }

  setCursor(form, index) {
    form._newSessionFormState.cursor = index;
    pointPickerCursor(this.input(form), this.options(form), index);
  }

  activate(form, option) {
    const { newSessionAction: action, newSessionPath: path, newSessionProject: project } = option.dataset;
    if (project || action === "folder" || action === "start") {
      form.querySelector("[data-new-session-cwd-value]").value = project || path;
      form.requestSubmit();
    } else if (action === "more") {
      form._newSessionFormState.expanded = true;
      this.focusInput(form);
      this.render(form, RECENT_PROJECTS);
    } else if (action === "path") {
      // New projects usually sit beside existing ones, so the first project's folder is listed first.
      this.browse(form, this.parent(this.projects(form)[0].dataset.newSessionProject));
    } else if (action === "up") {
      this.browse(form, path);
    } else if (action === "back") {
      this.showProjects(form);
    }
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
    } else if (event.key === "Tab" && !event.shiftKey && !event.repeat && options[cursor]?.dataset.newSessionAction === "folder") {
      event.preventDefault();
      this.browse(form, options[cursor].dataset.newSessionPath);
    } else if (event.key === "Backspace" && !this.input(form).value && !event.repeat) {
      // Holding Backspace to clear the filter must not carry on up the folders.
      const up = form.querySelector('[data-new-session-action="up"]');
      if (up) this.activate(form, up);
    }
  }
}
