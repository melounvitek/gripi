import { pointPickerCursor } from "./dom.js";

const RECENT_SESSIONS = 5;
const MATCHING_SESSIONS = 8;

export class CommandPaletteController {
  constructor(document, callbacks) {
    this.document = document;
    this.callbacks = callbacks;
    this.modal = document.querySelector('[data-modal="command-palette-modal"]');
    if (!this.modal) return;
    this.input = this.modal.querySelector("[data-command-palette-input]");
    this.list = this.modal.querySelector("[data-command-palette-list]");
    this.status = this.modal.querySelector("[data-command-palette-status]");
    this.opening = 0;
    this.modal.addEventListener("input", () => this.render());
    this.modal.addEventListener("click", (event) => {
      // There is no close button, so a click beside the card closes.
      if (event.target === this.modal) this.callbacks.closeModal(this.modal);
      else this.activate(event.target.closest('[role="option"]'));
    });
    this.modal.addEventListener("keydown", (event) => this.handleKeydown(event));
  }

  // False when there is nothing to toggle: this page has no palette, or another dialog is open.
  toggle() {
    if (!this.modal) return false;
    if (!this.modal.hidden) this.callbacks.closeModal(this.modal);
    else if (this.callbacks.modalIsOpen()) return false;
    else this.open();
    return true;
  }

  async open() {
    const opening = ++this.opening;
    this.input.value = "";
    // Rows appear only once the sessions arrive, so none moves under the cursor afterwards.
    this.sessions = null;
    this.enterPending = false;
    this.render();
    this.callbacks.openModal(this.modal);
    let sessions = null;
    try {
      const response = await fetch("/sessions/palette", { headers: { "Accept": "application/json" } });
      if (response.ok) sessions = (await response.json()).sessions;
    } catch (_error) {}
    if (opening !== this.opening || this.modal.hidden) return;
    this.sessions = sessions || [];
    this.failed = !sessions;
    this.render();
    // Without the sessions the first row is a command, which a waiting Enter never meant.
    if (this.enterPending && !this.failed) this.activate(this.options()[this.cursor]);
  }

  options() {
    return [...this.list.querySelectorAll('[role="option"]')];
  }

  row(mark, name, meta, run) {
    const row = this.document.createElement("button");
    row.type = "button";
    row.className = "picker-row command-palette-row";
    row.tabIndex = -1;
    row.setAttribute("role", "option");
    for (const [className, text, decorative] of [["picker-cursor", "→", true], ["project-monogram", mark, true], ["command-palette-name", name], ["command-palette-meta", meta]]) {
      const part = this.document.createElement("span");
      part.className = className;
      part.textContent = text;
      if (decorative) part.setAttribute("aria-hidden", "true");
      row.append(part);
    }
    this.runs.set(row, run);
    return row;
  }

  sessionRow(session) {
    const row = this.row(session.monogram, session.name, session.age, () => this.callbacks.openSession(session.path));
    row.title = session.project;
    if (session.unread) {
      row.classList.add("is-unread");
      const unread = this.document.createElement("span");
      unread.className = "visually-hidden";
      unread.textContent = "Unread: ";
      row.querySelector(".command-palette-name").prepend(unread);
    }
    row.querySelector(".project-monogram").style.setProperty("--project-identity-fg", session.color);
    if (session.busy) {
      const working = this.document.createElement("span");
      working.className = "session-running-indicator";
      working.setAttribute("role", "img");
      working.setAttribute("aria-label", "Pi is working");
      row.lastElementChild.replaceChildren(working);
    }
    return row;
  }

  commandRow({ label, detail, keys = "", run }) {
    const row = this.row("", label, keys, run);
    if (detail) {
      const slash = this.document.createElement("span");
      slash.className = "command-palette-detail";
      slash.textContent = detail;
      row.querySelector(".command-palette-name").append(" ", slash);
    }
    return row;
  }

  group(title, rows) {
    if (!rows.length) return;
    const group = this.document.createElement("div");
    group.setAttribute("role", "group");
    group.setAttribute("aria-label", title);
    const heading = this.document.createElement("p");
    heading.className = "command-palette-section";
    heading.setAttribute("aria-hidden", "true");
    heading.textContent = title;
    group.append(heading, ...rows);
    this.list.append(group);
  }

  render() {
    const words = this.input.value.toLowerCase().split(/\s+/).filter(Boolean);
    const matches = (text) => words.every((word) => text.toLowerCase().includes(word));
    this.list.replaceChildren();
    this.runs = new Map();
    let more = 0;
    if (this.sessions) {
      const current = this.callbacks.currentSessionPath();
      const matching = this.sessions.filter((session) => session.path !== current && matches(`${session.name}\n${session.project}`));
      // With nothing typed, the session this tab had open before leads, so Ctrl+K then Enter flips between two.
      const previous = this.callbacks.previousSessionPath();
      if (!words.length) matching.sort((left, right) => (right.path === previous) - (left.path === previous));
      const shown = matching.slice(0, words.length ? MATCHING_SESSIONS : RECENT_SESSIONS);
      more = matching.length - shown.length;
      this.group(words.length ? "Sessions" : "Recent sessions", shown.map((session) => this.sessionRow(session)));
      for (const [title, commands] of this.callbacks.commands()) {
        this.group(title, commands.filter((command) => command && matches(`${command.label} ${command.detail || ""}`)).map((command) => this.commandRow(command)));
      }
    }
    const options = this.options();
    options.forEach((option, index) => { option.id = `command-palette-option-${index}`; });
    this.setCursor(0);

    let status = "";
    if (!this.sessions) status = "Loading…";
    else if (this.failed) status = "Could not load sessions.";
    else if (!options.length) status = "No matches.";
    else if (more) status = `${more} more session${more === 1 ? "" : "s"} · ${words.length ? "keep typing" : "type"} to find them`;
    this.status.textContent = status;
    this.status.hidden = !status;
  }

  setCursor(index) {
    this.cursor = index;
    pointPickerCursor(this.input, this.options(), index);
  }

  activate(option) {
    const run = this.runs.get(option);
    if (!run) return;
    this.callbacks.closeModal(this.modal);
    run();
  }

  handleKeydown(event) {
    const count = this.options().length;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      if (count) this.setCursor((this.cursor + (event.key === "ArrowDown" ? 1 : count - 1)) % count);
    } else if (event.key === "Enter" && !event.isComposing) {
      event.preventDefault();
      // Enter can beat the sessions: it then opens the first match once they arrive.
      if (this.sessions) this.activate(this.options()[this.cursor]);
      else this.enterPending = true;
    }
  }
}
