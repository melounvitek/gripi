// Owns what the page shows about the worker that runs the session's repository: the marks in the header and the
// editor's footer, the block that takes the editor's place while the repository is copied there or the worker is lost,
// and the notice about main's files saved aside. It also moves the repository, which that block shows.
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

  // The state that the marks show: copying while the repository is copied there, gone once an unreachable worker must
  // have stopped its sessions, or null on this machine.
  indicator() {
    const location = this.location;
    if (!location) return null;
    if (location.copying) return "copying";
    if (location.state === "unreachable" && Date.parse(location.gone_at) <= Date.now()) return "gone";
    return location.state;
  }

  mode() {
    const state = this.indicator();
    if (this.pending) return this.pending.action === "move" ? "copying" : "returning";
    if (this.failure) return "failed";
    if (state === "copying") return "copying";
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
    badge.title = `Runs on ${this.location.worker} · ${state} · main's copy of ${this.repositoryName()} is read-only`;
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
    const facts = {
      copying: [
        ["moves", `${repository}, with its worktrees and sessions`],
        ["now", this.copyingStage()],
        ["on main", `${repository} stays read-only while it runs there`]
      ],
      returning: [["now", `stopping what its sessions run there and taking their last changes`]],
      failed: [["error", this.failure?.message]],
      unreachable: [
        ["steps", "any step that was running there ends with an unknown outcome"],
        ["next", `if it doesn't come back, Pi there stops by itself by ${clock(goneAt)}; then ${repository} can continue on main`]
      ],
      gone: [
        ["stopped", `Pi and everything it started there stopped by ${clock(goneAt)}`],
        ["on main", `${repository} and its sessions up to the last finished step`]
      ]
    }[mode];
    parts("title").textContent = {
      copying: `Copying ${repository} to ${worker}`,
      returning: `Bringing ${repository} back from ${worker}`,
      failed: this.failure?.action === "return" ? `Could not bring ${repository} back` : `Could not move ${repository}`,
      unreachable: `${worker} is not answering`,
      gone: `${worker} is gone`
    }[mode];
    parts("facts").replaceChildren(...facts.flatMap(([term, text]) => [this.element("dt", "", term), this.element("dd", "", text)]));
    parts("progress").hidden = !["copying", "returning"].includes(mode);
    const proceed = prompt.querySelector("[data-worker-continue]");
    const wait = prompt.querySelector("[data-worker-wait]");
    // Copying has nothing to answer; a failure only needs to be seen.
    prompt.querySelector("[data-worker-prompt-actions]").hidden = ["copying", "returning"].includes(mode);
    proceed.hidden = mode === "failed";
    wait.textContent = mode === "failed" ? "ok" : "keep waiting";
    wait.hidden = mode === "unreachable";
    parts("separator").hidden = mode !== "gone";
    parts("hint").textContent = mode === "gone" ? "enter continue · esc keep waiting" : mode === "failed" ? "esc ok" : "";
    const tick = () => {
      if (mode === "unreachable") {
        const left = Math.ceil((goneAt - Date.now()) / 1000);
        if (left <= 0) {
          this.render();
          return;
        }
        proceed.disabled = true;
        proceed.replaceChildren("continue on main in ", this.element("strong", "", duration(left)));
      } else if (["copying", "returning"].includes(mode)) {
        const since = Date.parse(location?.copying || "") || this.pending?.since || Date.now();
        parts("time").textContent = duration(Math.max(0, Math.floor((Date.now() - since) / 1000)));
      }
    };
    proceed.disabled = false;
    proceed.replaceChildren("continue on main");
    parts("time").textContent = "";
    tick();
    if (["unreachable", "copying", "returning"].includes(mode)) this.ticker = setInterval(tick, 1000);
    // Enter answers the block, as it would the editor that it replaced.
    if (this.focusedMode !== mode) {
      this.focusedMode = mode;
      const target = mode === "gone" ? proceed : mode === "failed" ? wait : null;
      if (target && this.callbacks.canFocus()) target.focus({ preventScroll: true });
    }
  }

  copyingStage() {
    const worker = this.location?.worker || this.pending?.worker;
    if (!this.location?.copying) return "stopping the repository's sessions here";
    return {
      connecting: `connecting to ${worker}, and copying Pi there the first time`,
      copying: "copying the repository and its sessions",
      connected: "starting to mirror it back to main"
    }[this.location.state] || this.location.state;
  }

  repositoryName() {
    const path = this.location?.repository || (this.pending || this.failure)?.repository || "";
    return path.split("/").filter(Boolean).pop() || "the repository";
  }

  renderNotice() {
    const { notice } = this.elements;
    if (!notice) return;
    const notices = this.notices;
    notice.hidden = !notices.length;
    if (!notices.length) return;
    const paths = [...new Set(notices.flatMap((each) => each.paths || []))];
    const repository = notices[0].repository || "";
    const name = repository.split("/").filter(Boolean).pop() || "the repository";
    const worker = this.location?.worker || "the worker";
    const shown = paths.slice(0, 3).map((path) => (path.startsWith(`${repository}/`) ? path.slice(repository.length + 1) : this.callbacks.homePath(path)));
    if (paths.length > shown.length) shown.push(`${paths.length - shown.length} more`);
    const latest = notices[notices.length - 1];
    notice.querySelector("[data-worker-notice-title]").textContent =
      `${paths.length} file${paths.length === 1 ? "" : "s"} changed on main while ${name} ran on ${worker}.`;
    notice.querySelector("[data-worker-notice-text]").textContent =
      `${worker}'s version replaced ${paths.length === 1 ? "it" : "them"}. Main's version of ${shown.slice(0, -1).join(", ")}${shown.length > 1 ? " and " : ""}${shown.at(-1)} ` +
      `is saved in ${this.callbacks.homePath(latest.aside)}${notices.length > 1 ? ` and ${notices.length - 1} earlier folder${notices.length > 2 ? "s" : ""} beside it` : ""}, at ${paths.length === 1 ? "its path" : "their paths"}.`;
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

function duration(seconds) {
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
}

function clock(date) {
  return date ? date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23" }) : "then";
}
