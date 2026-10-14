import { pointPickerCursor } from "./dom.js";
import { recentSessionShortcutFromEvent } from "./shortcuts.js";

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
      if (event.target === this.modal) {
        this.callbacks.closeModal(this.modal);
        return;
      }
      // On the page, the click would close a list that the command opens, such as the project filter's.
      event.stopPropagation();
      this.activate(event.target.closest('[role="option"]'));
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
    this.pending = null;
    this.piCommands = null;
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
    // Without the sessions every row is a command, which a waiting Enter or digit never meant.
    if (this.pending && !this.failed) this.activate(this.pending());
  }

  // Listing Pi's skills, prompt templates and extension commands starts the session's Pi, so as in the composer, only a "/" loads them.
  async loadPiCommands() {
    const opening = this.opening;
    this.piCommands = [];
    const commands = await this.callbacks.piCommands().catch(() => []);
    if (opening !== this.opening || this.modal.hidden || !commands.length) return;
    this.piCommands = commands;
    // Added below the rows shown, which stay, so the cursor and a mouse button pressed on a row stay on theirs.
    this.commandGroup("Pi commands", commands);
    this.finish(this.cursor);
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
    for (const [className, text, decorative] of [["picker-cursor", "→", true], ["project-monogram", mark, true], ["command-palette-name", name], ["command-palette-meta", meta], ["command-palette-key", "", true]]) {
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
    this.rowSessions.set(row, session);
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
      row.querySelector(".command-palette-meta").replaceChildren(working);
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

  matches(text) {
    return this.words.every((word) => text.toLowerCase().includes(word));
  }

  commandGroup(title, commands) {
    this.group(title, commands.filter((command) => command && this.matches(`${command.label} ${command.detail || ""}`)).map((command) => this.commandRow(command)));
  }

  render() {
    const words = this.input.value.toLowerCase().split(/\s+/).filter(Boolean);
    this.words = words;
    this.list.replaceChildren();
    this.runs = new Map();
    this.rowSessions = new Map();
    this.more = 0;
    if (this.sessions) {
      const current = this.callbacks.currentSessionPath();
      const matching = this.sessions.filter((session) => session.path !== current && this.matches(`${session.name}\n${session.project}`));
      // Unread sessions lead, however old. With nothing typed, the session this tab had open before follows them, so with nothing
      // unread, Ctrl+K then Enter flips between two.
      const previous = words.length ? null : this.callbacks.previousSessionPath();
      const rank = (session) => (session.unread ? 2 : session.path === previous ? 1 : 0);
      matching.sort((left, right) => rank(right) - rank(left));
      const shown = matching.slice(0, words.length ? MATCHING_SESSIONS : RECENT_SESSIONS);
      this.more = matching.length - shown.length;
      // A leading group comes before the sessions once something is typed, but not before Ctrl+K and Enter flips sessions.
      const groups = [...this.callbacks.commands().filter(Boolean), ["Pi commands", this.piCommands || []]];
      const leading = (group) => words.length && group[2]?.lead;
      for (const [title, commands] of groups.filter(leading)) this.commandGroup(title, commands);
      this.group(words.length ? "Sessions" : "Recent sessions", shown.map((session) => this.sessionRow(session)));
      if (!this.piCommands && this.input.value.trimStart().startsWith("/")) this.loadPiCommands();
      for (const [title, commands] of groups.filter((group) => !leading(group))) this.commandGroup(title, commands);
    }
    this.finish(0);
  }

  // Numbers the rows for Ctrl and a digit, and says what the list leaves out.
  finish(cursor) {
    const options = this.options();
    options.forEach((option, index) => {
      option.id = `command-palette-option-${index}`;
      option.querySelector(".command-palette-key").textContent = index < 9 ? index + 1 : "";
    });
    this.setCursor(cursor);

    let status = "";
    if (!this.sessions) status = "Loading…";
    else if (this.failed) status = "Could not load sessions.";
    else if (!options.length) status = "No matches.";
    else if (this.more) status = `${this.more} more session${this.more === 1 ? "" : "s"} · ${this.words.length ? "keep typing" : "type"} to find them`;
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
    const shortcut = event.ctrlKey && !event.altKey && !event.metaKey ? recentSessionShortcutFromEvent(event) : null;
    if (shortcut) {
      event.preventDefault();
      if (event.repeat) return;
      if (this.sessions) this.activate(this.options()[shortcut - 1]);
      else this.pending = () => this.options()[shortcut - 1];
    } else if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      if (count) this.setCursor((this.cursor + (event.key === "ArrowDown" ? 1 : count - 1)) % count);
    } else if (event.key === "Enter" && !event.isComposing) {
      event.preventDefault();
      // Enter can beat the sessions: it then opens the first match once they arrive.
      if (this.sessions) this.activate(this.options()[this.cursor]);
      else this.pending = () => this.options()[this.cursor];
    } else if (event.ctrlKey && !event.altKey && !event.metaKey && !event.shiftKey && event.key.toLowerCase() === "d") {
      // As in Pi CLI's session picker. The session open here is not listed, and like the sidebar's menu, a running one is refused.
      event.preventDefault();
      const session = this.rowSessions.get(this.options()[this.cursor]);
      if (!session) return;
      if (session.busy) {
        this.status.textContent = "Cannot delete a running session";
        this.status.hidden = false;
        return;
      }
      this.callbacks.closeModal(this.modal);
      this.callbacks.deleteSession(session);
    }
  }
}
