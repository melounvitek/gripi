import { movePickerCursor } from "./dom.js";

const DOTS = "••••••••";
// What lets Pi act as the user on GitHub: each comment with the lines it explains.
const GITHUB_GROUPS = [
  ["# gh: pull requests, issues, API", "GH_TOKEN="],
  ["# your name and email on commits", "GIT_AUTHOR_NAME=", "GIT_AUTHOR_EMAIL=", "GIT_COMMITTER_NAME=", "GIT_COMMITTER_EMAIL="],
  ["# git push over HTTPS with your token, also for SSH remotes; leave as is", "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=url.https://github.com/.insteadOf", "GIT_CONFIG_VALUE_0=git@github.com:", "GIT_CONFIG_KEY_1=credential.https://github.com.helper", "GIT_CONFIG_VALUE_1=!gh auth git-credential"]
];

// The GitHub lines whose names are not saved yet, under the comment of each group that still has one.
export function githubBlock(saved) {
  return GITHUB_GROUPS.flatMap(([comment, ...lines]) => {
    const missing = lines.filter((line) => !saved.includes(line.slice(0, line.indexOf("="))));
    return missing.length ? [comment, ...missing] : [];
  }).join("\n");
}

export class EnvironmentController {
  constructor(document, window, callbacks) {
    this.document = document;
    this.window = window;
    this.callbacks = callbacks;
    this.modal = document.querySelector('[data-modal="environment-modal"]');
    if (!this.modal) return;
    this.description = this.modal.querySelector("[data-environment-description]");
    this.list = this.modal.querySelector("[data-environment-list]");
    this.form = this.modal.querySelector("[data-environment-form]");
    this.nameInput = this.modal.querySelector("[data-environment-name]");
    this.valueInput = this.modal.querySelector("[data-environment-value]");
    this.removeButton = this.modal.querySelector("[data-environment-remove]");
    this.block = this.modal.querySelector("[data-environment-block]");
    this.text = this.modal.querySelector("[data-environment-text]");
    this.warning = this.modal.querySelector("[data-environment-warning]");
    this.status = this.modal.querySelector("[data-environment-status]");
    this.keys = this.modal.querySelector("[data-environment-keys]");
    // Null until the saved names arrive. Values are never kept: a row's value is fetched into its form.
    this.names = null;
    this.cursor = 0;
    this.operation = 0;
    this.modal.addEventListener("click", (event) => this.handleClick(event));
    this.modal.addEventListener("submit", (event) => {
      event.preventDefault();
      this.save(event.target);
    });
    this.nameInput.addEventListener("paste", (event) => this.handlePaste(event));
    this.text.addEventListener("input", () => this.resizeText());
    // On the document, so the keys still work after a click on the dialog's text took the focus off its controls.
    document.addEventListener("keydown", (event) => {
      if (!this.modal.hidden) this.handleKeydown(event);
    }, true);
  }

  async open() {
    this.names = null;
    this.cursor = 0;
    this.showList("Loading…");
    this.callbacks.openModal(this.modal);
    if (await this.request("/environment")) this.showList();
  }

  // Asks the gateway. Its error is shown as it is. An answer the dialog no longer waits for is dropped, unless it
  // is to a change: the change was made, so the list has to show it.
  async request(url, fields) {
    const operation = ++this.operation;
    let payload = null;
    let ok = false;
    try {
      const response = await fetch(url, { headers: { "Accept": "application/json" }, ...(fields && { method: "POST", body: new URLSearchParams(fields) }) });
      payload = await response.json().catch(() => null);
      ok = response.ok && !!payload;
    } catch (_error) {}
    if (operation !== this.operation && !fields) return null;
    if (!ok) {
      this.setStatus(payload?.error || "Could not read or save environment variables. Try again.", "error");
      return null;
    }
    // Every answer but a value lists the saved names.
    if (payload.variables) this.names = payload.variables.map(({ name }) => name);
    return payload;
  }

  setStatus(text, state = "") {
    this.status.textContent = text;
    this.status.hidden = !text;
    this.status.dataset.state = state;
  }

  part(className, text) {
    const part = this.document.createElement("span");
    part.className = className;
    part.textContent = text;
    return part;
  }

  row(label, run) {
    const row = this.document.createElement("button");
    row.type = "button";
    row.className = "picker-row picker-option environment-row";
    row.setAttribute("role", "option");
    const cursor = this.part("picker-cursor", "→");
    cursor.setAttribute("aria-hidden", "true");
    row.append(cursor, this.part("environment-name", label));
    this.runs.set(row, run);
    this.list.append(row);
    return row;
  }

  // Closes the form and the block, lists the saved names and puts the cursor back.
  showList(message = "", state = "") {
    this.closeForm();
    this.list.replaceChildren();
    this.runs = new Map();
    const github = this.names ? githubBlock(this.names) : "";
    if (this.names) {
      for (const name of this.names) {
        const value = this.part("environment-value", DOTS);
        value.setAttribute("aria-hidden", "true");
        this.row(name, () => this.edit(name)).append(value);
      }
      this.row("+ add variable", () => this.edit("")).classList.add("environment-add");
      if (github) {
        const none = github === githubBlock([]);
        const row = this.row(none ? "+ add GitHub variables" : "+ add the rest for GitHub", () => this.openBlock(github, true));
        row.classList.add("environment-add");
        if (none) row.lastChild.append(" ", this.part("environment-add-detail", "gh, commits and push as you"));
      }
    }
    this.description.hidden = this.list.hidden = this.keys.hidden = false;
    this.warning.hidden = !github || !this.names.includes("GH_TOKEN");
    this.setStatus(message || (this.names?.length ? "Changes apply from your next message in each session." : "Nothing set yet."), state);
    movePickerCursor(this.list, this.cursor);
  }

  // Also runs when the dialog closes, so no value outlives the form that showed it.
  closeForm() {
    this.operation += 1;
    this.form.hidden = this.block.hidden = true;
    this.form.reset();
    this.block.reset();
  }

  // The form opens under the list; the block takes the list's place.
  showEditor(editor, field) {
    editor.hidden = false;
    this.description.hidden = this.list.hidden = editor === this.block;
    this.keys.hidden = this.warning.hidden = true;
    this.setStatus("");
    // On touch screens focusing the field would raise the keyboard over what just opened.
    if (this.window.matchMedia?.("(pointer: fine)").matches !== false) field.focus();
  }

  async edit(name) {
    // Another row's form may still be open.
    this.showList();
    let value = "";
    if (name) {
      const payload = await this.request(`/environment/value?name=${encodeURIComponent(name)}`);
      if (!payload) return;
      value = payload.value;
    }
    this.previousName = name;
    this.nameInput.value = name;
    this.valueInput.value = value;
    this.removeButton.hidden = this.removeButton.nextElementSibling.hidden = !name;
    this.showEditor(this.form, name ? this.valueInput : this.nameInput);
  }

  openBlock(text, github = false) {
    this.closeForm();
    this.block.querySelectorAll("[data-environment-github]").forEach((part) => { part.hidden = !github; });
    this.block.querySelector("[data-environment-pasted]").hidden = github;
    this.text.value = text;
    this.showEditor(this.block, this.text);
    this.resizeText();
    // The caret waits at the first value to fill in.
    const empty = text.search(/=$/m) + 1;
    if (empty) this.text.setSelectionRange(empty, empty);
  }

  // The block grows with its lines instead of scrolling inside the dialog.
  resizeText() {
    this.text.style.height = "auto";
    this.text.style.height = `${this.text.scrollHeight + this.text.offsetHeight - this.text.clientHeight}px`;
  }

  async save(form) {
    const block = form === this.block;
    const payload = block
      ? await this.request("/environment/variables", { text: this.text.value })
      : await this.request("/environment/variable", { name: this.nameInput.value, value: this.valueInput.value, previous_name: this.previousName });
    if (!payload) return;
    // A block leaves the cursor on "+ add variable"; a single variable keeps it on its row.
    if (block) this.cursor = this.names.length;
    this.showList(block && payload.saved !== 1 ? `Saved ${payload.saved} variables · used from your next message.` : "Saved · used from your next message.", "done");
  }

  async remove() {
    if (await this.request("/environment/variable/delete", { name: this.previousName })) this.showList("Removed · applies from your next message.", "done");
  }

  handleClick(event) {
    const row = event.target.closest('[role="option"]');
    if (row) {
      this.cursor = [...this.list.children].indexOf(row);
      this.runs.get(row)();
    } else if (event.target.closest("[data-environment-cancel]")) {
      this.showList();
    } else if (event.target.closest("[data-environment-remove]")) {
      this.remove();
    }
  }

  handlePaste(event) {
    const text = event.clipboardData.getData("text");
    const lines = text.split("\n").filter((line) => line.trim());
    if (lines.length > 1) {
      event.preventDefault();
      this.openBlock(text.trim());
    } else if (text.includes("=")) {
      event.preventDefault();
      const line = text.trim();
      const cut = line.indexOf("=");
      this.nameInput.value = line.slice(0, cut).trim();
      // Like a line of the block, the value loses one pair of quotes around it.
      this.valueInput.value = line.slice(cut + 1).trim().replace(/^(["'])(.*)\1$/, "$2");
      this.valueInput.focus();
    }
  }

  handleKeydown(event) {
    const editing = !this.form.hidden || !this.block.hidden;
    if (event.key === "Escape" && editing) {
      // Handled here, the page does not go on to close the whole dialog.
      event.preventDefault();
      this.showList();
    } else if (event.key === "Enter" && (event.ctrlKey || event.metaKey) && !this.block.hidden) {
      event.preventDefault();
      this.block.requestSubmit();
    } else if ((event.key === "ArrowDown" || event.key === "ArrowUp") && !editing && this.list.children.length) {
      event.preventDefault();
      const count = this.list.children.length;
      this.cursor = (this.cursor + (event.key === "ArrowDown" ? 1 : count - 1)) % count;
      movePickerCursor(this.list, this.cursor);
    }
  }
}
