// Owns what the page shows about the worker that runs the session on a private copy of its repository: the marks in
// the header and the editor's footer, the block that takes the editor's place while the session moves there or back or
// the worker is lost, and the notice about what its returns kept aside. It also moves the session, which that block
// shows.
export class WorkerController {
  constructor(document, window, callbacks) {
    this.document = document;
    this.window = window;
    // sessionPath, canFocus, refresh (poll the session now), stateChanged (the worker's state changed), homePath,
    // failed (show an error that no block shows).
    this.callbacks = callbacks;
    this.elements = null;
    this.location = null;
    this.notices = [];
    // A move or return that this page asked for and waits for, and the error of the last one, until dismissed.
    this.pending = null;
    this.failure = null;
    // The gone worker whose block "keep waiting" put away.
    this.waited = "";
    this.ticker = null;
    this.focusedMode = null;
  }

  bind({ badge, prompt, form, statusBar, notice }) {
    this.elements = { badge, prompt, form, statusBar, notice, status: null };
    if (prompt) {
      prompt.addEventListener("keydown", (event) => this.keydown(event));
      prompt.querySelector("[data-worker-continue]").addEventListener("click", () => this.continueOnMain());
      prompt.querySelector("[data-worker-wait]").addEventListener("click", () => this.keepWaiting());
    }
    notice?.querySelector("[data-worker-notice-dismiss]").addEventListener("click", () => this.dismissNotices());
  }

  // Takes where the session runs and the notices, from its page or its live state.
  update(location, notices) {
    const before = this.indicator();
    this.location = location || null;
    this.notices = Array.isArray(notices) ? notices : [];
    if (before !== null && before !== this.indicator()) this.callbacks.stateChanged();
    this.render();
  }

  // Forgets the session before its view is replaced. A move that it asked for goes on.
  reset() {
    clearInterval(this.ticker);
    this.ticker = null;
    this.elements = null;
    this.location = null;
    this.notices = [];
    this.pending = null;
    this.failure = null;
  }

  // Whether the block has the editor's place.
  get shown() {
    return !!this.mode();
  }

  // The state that the marks show: copying while the session moves there, returning while it comes back, gone once an
  // unreachable worker must have stopped it, or null on this machine.
  indicator() {
    const location = this.location;
    if (!location) return null;
    if (location.copying) return "copying";
    if (location.state === "unreachable" && Date.parse(location.gone_at) <= Date.now()) return "gone";
    return location.state;
  }

  mode() {
    const state = this.indicator();
    // The gateway names the session that a move or return waits for only while it waits.
    if (this.location?.waiting) return "waiting";
    if (this.pending) return this.pending.action === "move" ? "copying" : "returning";
    if (this.failure) return "failed";
    if (state === "copying") return "copying";
    if (state === "returning") return this.location.returning ? "returning" : "unfinished";
    if (state === "unreachable") return "unreachable";
    if (state === "gone" && this.waited !== this.goneKey()) return "gone";
    return null;
  }

  goneKey() {
    return `${this.location?.worker} ${this.location?.gone_at}`;
  }

  render() {
    if (!this.elements) return;
    this.renderBadge();
    this.renderStatus();
    this.renderPrompt();
    this.renderNotice();
  }

  renderBadge() {
    const { badge } = this.elements;
    if (!badge) return;
    const state = this.indicator();
    badge.hidden = !state;
    if (!state) return;
    badge.dataset.workerState = state;
    badge.title = `Runs on ${this.location.worker} · ${state} · on a private copy of ${this.repositoryName()}`;
    badge.querySelector("[data-worker-badge-name]").textContent = this.location.worker;
    // Quiet while all is well.
    badge.querySelector("[data-worker-badge-state]").textContent = ["connected", "disconnected"].includes(state) ? "" : state;
  }

  // Like a Pi extension's status: the worker's name, its state and the ports that main forwards.
  renderStatus() {
    const { statusBar } = this.elements;
    if (!statusBar) return;
    const state = this.indicator();
    if (!state) {
      this.elements.status?.remove();
      this.elements.status = null;
      return;
    }
    let item = this.elements.status;
    if (!item) {
      item = this.elements.status = this.document.createElement("span");
      item.className = "session-status-item";
      item.dataset.statusKey = "worker";
      statusBar.append(item);
    }
    const name = this.element("span", "session-status-label", this.location.worker);
    const value = this.element("span", "session-status-value");
    if (state !== "disconnected") {
      const word = this.element("span", "worker-status-state", state);
      word.dataset.workerState = state;
      value.append(word);
    }
    const ports = this.location.ports || [];
    if (ports.length) {
      const list = ports.map(({ port, local }) => (port === local ? `${port}` : `${port}→${local}`)).join(", ");
      value.append(this.element("span", "worker-status-ports", `${state === "disconnected" ? "" : " · "}port${ports.length > 1 ? "s" : ""} ${list}`));
    }
    item.replaceChildren(name, " ", value);
  }

  renderPrompt() {
    const { prompt, form } = this.elements;
    if (!prompt) return;
    const mode = this.mode();
    prompt.hidden = !mode;
    form?.toggleAttribute("data-worker", !!mode);
    clearInterval(this.ticker);
    this.ticker = null;
    if (!mode) {
      this.focusedMode = null;
      return;
    }
    prompt.dataset.mode = mode;
    const location = this.location;
    const worker = location?.worker || (this.pending || this.failure)?.worker || "the worker";
    const repository = this.repositoryName();
    const parts = (name) => prompt.querySelector(`[data-worker-prompt-${name}]`);
    const goneAt = location?.gone_at ? new Date(location.gone_at) : null;
    const returning = this.pending ? this.pending.action === "return" : !location?.copying;
    const facts = {
      waiting: [
        ["then", returning ? `this session comes back from ${worker} with its changes` : `this session moves to ${worker}, on a private copy of ${repository}`],
        ["on main", `new prompts to ${repository}'s sessions wait meanwhile`]
      ],
      copying: [
        ["copy", `a private copy of ${repository}, for this session only`],
        ["now", this.copyingStage()],
        ["on main", `${repository}'s other sessions keep running`]
      ],
      returning: [
        ["now", `stopping it there, then bringing its changes to ${repository} on main`],
        ["clashes", `main keeps what it changed too; ${worker}'s version is kept aside`]
      ],
      unfinished: [["now", "it doesn't start until its return finishes"], ["next", "continue on main finishes it"]],
      failed: [["error", this.failure?.message]],
      unreachable: [
        ["steps", "any step that was running there ends with an unknown outcome"],
        ["next", `if it doesn't come back, Pi there stops by itself by ${clock(goneAt)}; then this session can continue on main`]
      ],
      gone: [
        ["stopped", `Pi and everything it started there stopped by ${clock(goneAt)}`],
        ["on main", "this session, with its changes up to the last step that main confirmed"]
      ]
    }[mode];
    parts("title").textContent = {
      waiting: `Waiting for “${shortName(location?.waiting)}” to finish`,
      copying: `Moving this session to ${worker}`,
      returning: `Bringing this session back from ${worker}`,
      unfinished: `Bringing this session back from ${worker} didn't finish`,
      failed: this.failure?.action === "return" ? "Could not bring this session back" : "Could not move this session",
      unreachable: `${worker} is not answering`,
      gone: `${worker} is gone`
    }[mode];
    parts("facts").replaceChildren(...facts.flatMap(([term, text]) => [this.element("dt", "", term), this.element("dd", "", text)]));
    const running = ["waiting", "copying", "returning"].includes(mode);
    parts("progress").hidden = !running || mode === "waiting";
    const proceed = prompt.querySelector("[data-worker-continue]");
    const wait = prompt.querySelector("[data-worker-wait]");
    // A move or return that runs has nothing to answer; a failure only needs to be seen.
    prompt.querySelector("[data-worker-prompt-actions]").hidden = running;
    proceed.hidden = mode === "failed";
    wait.textContent = mode === "failed" ? "ok" : "keep waiting";
    wait.hidden = ["unreachable", "unfinished"].includes(mode);
    parts("separator").hidden = mode !== "gone";
    parts("hint").textContent = { gone: "enter continue · esc keep waiting", unfinished: "enter continue", failed: "esc ok" }[mode] || "";
    const tick = () => {
      if (mode === "unreachable") {
        const left = Math.ceil((goneAt - Date.now()) / 1000);
        if (left <= 0) {
          this.render();
          return;
        }
        proceed.disabled = true;
        proceed.replaceChildren("continue on main in ", this.element("strong", "", duration(left)));
      } else if (running) {
        const since = Date.parse(location?.copying || location?.returning || "") || this.pending?.since || Date.now();
        parts("time").textContent = duration(Math.max(0, Math.floor((Date.now() - since) / 1000)));
      }
    };
    proceed.disabled = false;
    proceed.replaceChildren("continue on main");
    parts("time").textContent = "";
    tick();
    if (running || mode === "unreachable") this.ticker = setInterval(tick, 1000);
    // Enter answers the block, as it would the editor that it replaced.
    if (this.focusedMode !== mode) {
      this.focusedMode = mode;
      const target = ["gone", "unfinished"].includes(mode) ? proceed : mode === "failed" ? wait : null;
      if (target && this.callbacks.canFocus()) target.focus({ preventScroll: true });
    }
  }

  // The worker's state says how far a move has got only while it connects to the worker or copies there.
  copyingStage() {
    return {
      connecting: `connecting to ${this.location?.worker}, and copying Pi there the first time`,
      copying: `copying the session and its copy of ${this.repositoryName()} there`
    }[this.location?.copying && this.location.state] || "preparing the copy";
  }

  repositoryName() {
    const path = this.location?.repository || (this.pending || this.failure)?.repository || "";
    return path.split("/").filter(Boolean).pop() || "the repository";
  }

  // What the session's returns kept aside, where main kept its own version: the paths and where the worker's
  // versions are, with the details of each return, which say why.
  renderNotice() {
    const { notice } = this.elements;
    if (!notice) return;
    const notices = this.notices;
    notice.hidden = !notices.length;
    if (!notices.length) return;
    const shown = notices.flatMap(({ repository, items }) => items.map(({ path }) => (path.startsWith(`${repository}/`) ? path.slice(repository.length + 1) : this.callbacks.homePath(path))));
    const folders = this.asideFolders();
    const workers = [...new Set(notices.map(({ worker }) => worker))];
    const show = (name, text) => {
      const element = notice.querySelector(`[data-worker-notice-${name}]`);
      // A poll that changes nothing keeps what the user selected there, such as a ref to copy.
      if (element.textContent !== text) element.textContent = text;
    };
    show("title", `${shown.length} change${shown.length === 1 ? "" : "s"} from ${list(workers, 2)} kept aside.`);
    show("text", `Main kept its own ${list(shown, 3)}. The worker's versions, if any, are in ${list([...folders, ...notices.flatMap(({ refs }) => refs)], folders.length + 2)}.`);
    show("details", notices.map(({ text }) => text).join("\n\n"));
  }

  asideFolders() {
    return [...new Set(this.notices.map(({ aside }) => this.callbacks.homePath(aside)))];
  }

  keydown(event) {
    if (event.isComposing) return;
    const mode = this.mode();
    if (event.key === "Escape" && ["gone", "failed"].includes(mode)) {
      // Also keeps Escape from stopping the agent.
      event.preventDefault();
      this.keepWaiting();
    }
  }

  keepWaiting() {
    if (this.failure) this.failure = null;
    else this.waited = this.goneKey();
    this.render();
  }

  move(worker, repository) {
    return this.send("move", "/workers/move", { worker }, { worker, repository });
  }

  continueOnMain() {
    return this.send("return", "/workers/return", {}, { worker: this.location?.worker, repository: this.location?.repository });
  }

  // Asks the gateway to move the repository, which the block shows until the gateway answers.
  async send(action, url, fields, details) {
    if (this.pending) return false;
    const session = this.callbacks.sessionPath();
    const pending = { action, since: Date.now(), ...details };
    this.pending = pending;
    this.failure = null;
    this.render();
    this.callbacks.refresh();
    const message = await this.post(url, { session, ...fields });
    if (this.pending !== pending) return message === "";
    this.pending = null;
    this.failure = message ? { action, message, ...details } : null;
    this.render();
    this.callbacks.refresh();
    return message === "";
  }

  async forward(port) {
    return this.post("/workers/forward", { session: this.callbacks.sessionPath(), port: String(port) }, true);
  }

  async unforward(port) {
    const message = await this.post("/workers/unforward", { session: this.callbacks.sessionPath(), port: String(port) });
    if (message) this.callbacks.failed(message);
    this.callbacks.refresh();
  }

  async dismissNotices() {
    const folders = this.asideFolders();
    if (!this.window.confirm(`Dismissing also deletes what was kept aside: the worker's versions in ${list(folders, folders.length)} and its recovery refs. Main's files stay as they are.`)) return;
    const message = await this.post("/workers/dismiss_notices", { session: this.callbacks.sessionPath() });
    if (message) {
      this.callbacks.failed(message);
      return;
    }
    this.notices = [];
    this.render();
  }

  // Posts a form and returns the gateway's error, or "" once it is done. With answer, it returns the answer instead
  // of "", which an error has as its error.
  async post(url, fields, answer = false) {
    try {
      const response = await this.window.fetch(url, { method: "POST", body: new URLSearchParams(fields), headers: { Accept: "application/json" } });
      const payload = await response.json().catch(() => ({}));
      if (response.ok) return answer ? payload : "";
      return answer ? { error: payload.error || "The gateway refused." } : payload.error || "The gateway refused.";
    } catch (_error) {
      const error = "Could not reach the gateway. Try again.";
      return answer ? { error } : error;
    }
  }

  element(tagName, className, text = "") {
    const element = this.document.createElement(tagName);
    if (className) element.className = className;
    element.textContent = text;
    return element;
  }
}

// Names a session, such as by its first message, in a line.
function shortName(name = "") {
  const line = name.replace(/\s+/g, " ").trim();
  return line.length > 48 ? `${line.slice(0, 48).trimEnd()}…` : line;
}

// Lists the first of some names, and how many more there are: "a, b and 2 more".
function list(names, shown) {
  const listed = names.length > shown ? [...names.slice(0, shown), `${names.length - shown} more`] : names;
  return listed.length > 1 ? `${listed.slice(0, -1).join(", ")} and ${listed.at(-1)}` : listed[0];
}

function duration(seconds) {
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
}

function clock(date) {
  return date ? date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23" }) : "then";
}
