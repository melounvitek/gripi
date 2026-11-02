import { movePickerCursor } from "./dom.js";

// Owns the dialog that picks a worker to run the session on, which says what moves with it before it moves, and the
// one that forwards a port of the worker. The WorkerController sends what they ask for.
export class WorkerPickerController {
  constructor(document, window, callbacks) {
    this.document = document;
    this.window = window;
    // openModal, closeModal, sessionPath, homePath, move(worker, repository), forward(port) → {port, local} or
    // {error}, refresh.
    this.callbacks = callbacks;
    this.modal = document.querySelector('[data-modal="worker-modal"]');
    if (!this.modal) return;
    this.title = this.modal.querySelector("[data-worker-modal-title]");
    this.list = this.modal.querySelector("[data-worker-picker]");
    this.portField = this.modal.querySelector("[data-worker-port-field]");
    this.port = this.modal.querySelector("[data-worker-port]");
    this.facts = this.modal.querySelector("[data-worker-facts]");
    this.status = this.modal.querySelector("[data-worker-status]");
    this.go = this.modal.querySelector("[data-worker-go]");
    this.hint = this.modal.querySelector("[data-worker-hint]");
    this.mode = null;
    this.workers = [];
    this.repository = null;
    this.cursor = 0;
    this.opening = 0;
    this.sending = false;
    // A tap points at a worker, and the move waits for its own button, because it stops the session's Pi.
    this.list.addEventListener("click", (event) => {
      const row = event.target.closest('[role="option"]');
      if (row) this.choose([...this.list.querySelectorAll('[role="option"]')].indexOf(row));
    });
    this.go.addEventListener("click", () => this.activate());
    this.port.addEventListener("input", () => this.renderForward());
    this.modal.addEventListener("keydown", (event) => this.keydown(event));
  }

  // Lists the workers, with what the session's private copy of its repository gets.
  async openMove() {
    const opening = this.open("move", "Run on worker");
    this.setStatus("Loading workers…");
    let payload = null;
    try {
      const response = await this.window.fetch(`/workers?session=${encodeURIComponent(this.callbacks.sessionPath())}`, { headers: { Accept: "application/json" } });
      payload = await response.json().catch(() => null);
      if (!response.ok) payload = { error: payload?.error || "Could not list the workers." };
    } catch (_error) {
      payload = { error: "Could not reach the gateway. Try again." };
    }
    if (opening !== this.opening || this.modal.hidden) return;
    if (payload.error) {
      this.setStatus(payload.error, true);
      return;
    }
    this.workers = payload.workers || [];
    this.repository = payload.repository;
    if (!this.workers.length) {
      this.setStatus("No workers yet. Add one on the gateway's machine with gripi worker add user@host.");
      return;
    }
    this.setStatus("");
    this.list.replaceChildren(...this.workers.map((worker) => this.row(worker)));
    const { main, roots } = this.repository;
    const name = main.split("/").filter(Boolean).pop();
    const copied = roots.map((root) => (root === main ? name : this.callbacks.homePath(root)));
    const listed = copied.length > 1 ? `${copied.slice(0, -1).join(", ")} and ${copied.at(-1)}` : copied[0];
    this.renderFacts([
      ["moves", `this session only, to a private copy of ${name}`],
      // The main checkout holds the Git folder, which comes along without it too.
      ["copy", roots.includes(main) ? listed : `${listed}, with ${name}'s Git folder`],
      ["ignored", "files up to 10 MB; folders such as node_modules/ come from the worker's earlier copies"],
      ["on main", `${name}'s other sessions keep running`],
      ["return", "brings its changes back; what main changed too is kept aside"]
    ]);
    this.go.hidden = false;
    this.choose(Math.max(0, this.workers.findIndex((worker) => worker.state === "connected")));
  }

  openForward(worker) {
    this.open("forward", "Forward a port");
    this.worker = worker;
    this.portField.hidden = false;
    this.port.value = "";
    this.go.hidden = false;
    this.go.textContent = "forward";
    this.hint.textContent = "enter forward · esc cancel";
    this.renderForward();
    this.port.focus();
  }

  open(mode, title) {
    this.mode = mode;
    this.sending = false;
    this.title.textContent = title;
    this.list.replaceChildren();
    this.facts.replaceChildren();
    this.portField.hidden = mode !== "forward";
    this.go.hidden = true;
    this.hint.textContent = mode === "move" ? "↑↓ choose · enter move · esc cancel" : "";
    this.setStatus("");
    this.callbacks.openModal(this.modal);
    return ++this.opening;
  }

  row(worker) {
    const row = this.document.createElement("button");
    row.type = "button";
    row.className = "picker-row worker-picker-row";
    row.tabIndex = -1;
    row.setAttribute("role", "option");
    row.dataset.workerState = worker.state;
    for (const [className, text] of [["picker-cursor", "→"], ["worker-picker-name", worker.name], ["worker-picker-destination", worker.destination], ["worker-picker-state", worker.state]]) {
      const part = this.document.createElement("span");
      part.className = className;
      part.textContent = text;
      if (className === "picker-cursor") part.setAttribute("aria-hidden", "true");
      row.append(part);
    }
    return row;
  }

  choose(index) {
    if (index < 0 || !this.workers.length) return;
    this.cursor = (index + this.workers.length) % this.workers.length;
    movePickerCursor(this.list, this.cursor);
    this.go.textContent = `move to ${this.workers[this.cursor].name}`;
  }

  renderForward() {
    const port = this.port.value.trim() || "port";
    this.renderFacts([
      ["from", `${this.worker}'s localhost:${port}`],
      ["to", `main's localhost:${port}, or the next free port`]
    ]);
  }

  renderFacts(facts) {
    this.facts.replaceChildren(...facts.flatMap(([term, text]) => {
      const name = this.document.createElement("dt");
      name.textContent = term;
      const value = this.document.createElement("dd");
      value.textContent = text;
      return [name, value];
    }));
  }

  setStatus(text, error = false) {
    this.status.textContent = text;
    this.status.hidden = !text;
    this.status.classList.toggle("is-error", error);
  }

  keydown(event) {
    if (event.isComposing) return;
    if (this.mode === "move" && (event.key === "ArrowDown" || event.key === "ArrowUp")) {
      event.preventDefault();
      this.choose(this.cursor + (event.key === "ArrowDown" ? 1 : -1));
    } else if (event.key === "Enter" && !event.target?.closest?.("[data-modal-close]")) {
      event.preventDefault();
      this.activate();
    }
  }

  async activate() {
    if (this.sending) return;
    if (this.mode === "move") {
      const worker = this.workers[this.cursor];
      if (!worker) return;
      this.callbacks.closeModal(this.modal);
      // The editor's place shows the move from here on.
      this.callbacks.move(worker.name, this.repository.main);
      return;
    }
    const port = Number(this.port.value.trim());
    if (!Number.isInteger(port) || port < 1 || port > 65535) {
      this.setStatus("Enter a port from 1 to 65535.", true);
      return;
    }
    this.sending = true;
    this.setStatus(`Forwarding port ${port}…`);
    const forwarded = await this.callbacks.forward(port);
    this.sending = false;
    if (this.modal.hidden || this.mode !== "forward") return;
    if (forwarded.error) {
      this.setStatus(forwarded.error, true);
      return;
    }
    this.callbacks.closeModal(this.modal);
    this.callbacks.refresh();
  }
}
