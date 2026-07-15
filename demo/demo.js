(function (global) {
  "use strict";

  const initialSessions = [
    {
      id: "welcome", name: "Welcome to GRIPi", project: "gripi", monogram: "GR", color: "#ff9b73", background: "#4a281f", age: "Start here", pinned: true,
      messages: [
        { role: "user", text: "What is GRIPi, and how can I get started?", time: "Welcome" },
        { role: "thinking", text: "I’ll give you a quick tour and the shortest path to a local installation.", time: "Welcome" },
        { role: "assistant", text: "GRIPi is a desktop and web portal for Pi, powered by a self-hosted gateway. Run the gateway on your development machine or home server, then use your existing Pi projects and sessions from the desktop app or a browser.\n\nThis static demo lets you explore session navigation, settings, streamed responses, and tool activity. Prompts stay in this browser, and all Pi or gateway behavior is simulated.", time: "Welcome" },
        { role: "tool", title: "Installation requirements", text: "mise\nPi CLI available on PATH", time: "Welcome" },
        { role: "assistant", text: "Clone and start GRIPi with these commands. Setup prints the admin password used to approve your browser, then GRIPi is available at http://localhost:4567.", code: "git clone https://github.com/melounvitek/gripi.git\ncd gripi\nmise install\nmise run setup\nGRIPI_HOST=127.0.0.1 mise run start", link: { href: "https://github.com/melounvitek/gripi", label: "View GRIPi on GitHub →" }, time: "Welcome" }
      ]
    },
    {
      id: "demo-ui", name: "Build responsive session sidebar", project: "gripi", monogram: "GR", color: "#ff9b73", background: "#4a281f", age: "2 minutes ago", pinned: false,
      messages: [
        { role: "user", text: "Can you make the session sidebar work well on mobile without changing the desktop layout?", time: "10:14" },
        { role: "thinking", text: "I’ll inspect the existing responsive rules and preserve the desktop grid.", time: "10:14" },
        { role: "tool", title: "read public/assets/app.css:1-240", text: "Read the layout, sidebar, and mobile breakpoint styles.\nFound the existing 760px breakpoint.\nVerified the desktop grid remains 22rem plus conversation.\nChecked focus and backdrop styles.\nCompared composer sizing at 390px.\nNo production selectors need to diverge.", time: "10:15" },
        { role: "assistant", text: "Implemented the responsive sidebar as an off-canvas panel below 760px. The desktop grid remains unchanged, while mobile gets a menu button, backdrop, and accessible close behavior.\n\nI also kept the conversation and composer usable at narrow widths.", time: "10:16" },
        { role: "compaction", title: "Conversation compacted", text: "The user requested a responsive session sidebar while preserving desktop behavior. Production structure and mobile interaction were reviewed and implemented.", time: "10:17" },
        { role: "error", text: "Demo example: a gateway error would appear here without leaving the composer spinning.", time: "10:17" }
      ]
    },
    { id: "api-errors", name: "Improve gateway error handling", project: "gripi", monogram: "GR", color: "#ff9b73", background: "#4a281f", age: "Yesterday", pinned: true, messages: [
      { role: "user", text: "When the gateway loses its Pi process, show a useful error instead of leaving the composer spinning.", time: "Yesterday" },
      { role: "assistant", text: "Updated both failure paths. A failed send restores the draft and displays the gateway error.", time: "Yesterday" }
    ] },
    { id: "release-notes", name: "Draft release notes", project: "website", monogram: "WE", color: "#9fc5ff", background: "#1e334d", age: "Monday", pinned: true, messages: [
      { role: "user", text: "Draft concise release notes from the changes in this branch.", time: "Monday" },
      { role: "assistant", text: "What’s new\n\n• Find sessions faster with sidebar search.\n• Enjoy a cleaner composer on mobile screens.\n• Keep unsent drafts when reconnecting.", time: "Monday" }
    ] },
    { id: "billing-spec", name: "Investigate flaky checkout spec", project: "storefront", monogram: "ST", color: "#b5e3b0", background: "#203c2b", age: "Last week", pinned: false, messages: [
      { role: "user", text: "The checkout browser spec fails about one run in twenty. Can you investigate without weakening its assertions?", time: "Last week" },
      { role: "thinking", text: "I’ll reproduce it repeatedly and compare browser state on failed runs.", time: "Last week" },
      { role: "tool", title: "bash mise run test", text: "148 runs, 612 assertions, 0 failures, 0 errors", time: "Last week" },
      { role: "assistant", text: "The assertion sometimes ran before the redirect completed. I used the framework’s waiting navigation assertion.", time: "Last week" }
    ] },
    { id: "markdown-rendering", name: "Fix streamed markdown rendering", project: "gripi", monogram: "GR", color: "#ff9b73", background: "#4a281f", age: "2 weeks ago", pinned: false, messages: [
      { role: "user", text: "Make streamed code blocks match the server-rendered history.", time: "2 weeks ago" },
      { role: "assistant", text: "Aligned the live renderer with the server markup and added regression coverage.", time: "2 weeks ago" }
    ] },
    { id: "keyboard-shortcuts", name: "Add session keyboard shortcuts", project: "gripi", monogram: "GR", color: "#ff9b73", background: "#4a281f", age: "3 weeks ago", pinned: false, messages: [
      { role: "user", text: "Add shortcuts for switching between recent sessions.", time: "3 weeks ago" },
      { role: "assistant", text: "Added number-key navigation with a visible shortcut overlay.", time: "3 weeks ago" }
    ] },
    { id: "docs-navigation", name: "Simplify documentation navigation", project: "website", monogram: "WE", color: "#9fc5ff", background: "#1e334d", age: "Last month", pinned: false, messages: [
      { role: "user", text: "Can you reorganize the setup and configuration guides?", time: "Last month" },
      { role: "assistant", text: "Reorganized the guides around installation, local use, and remote access.", time: "Last month" }
    ] },
    { id: "checkout-copy", name: "Polish checkout confirmation copy", project: "storefront", monogram: "ST", color: "#b5e3b0", background: "#203c2b", age: "Last month", pinned: false, messages: [
      { role: "user", text: "Make the confirmation screen clearer without adding more steps.", time: "Last month" },
      { role: "assistant", text: "Shortened the heading and surfaced the delivery estimate next to the order number.", time: "Last month" }
    ] },
    { id: "ci-cache", name: "Speed up CI dependency caching", project: "storefront", monogram: "ST", color: "#b5e3b0", background: "#203c2b", age: "2 months ago", pinned: false, messages: [
      { role: "user", text: "The test workflow spends too long installing unchanged dependencies.", time: "2 months ago" },
      { role: "assistant", text: "Updated the cache key to include only the relevant lockfiles and runtime version.", time: "2 months ago" }
    ] }
  ];

  function safeIdentityColor(value, fallback) {
    return /^#[0-9a-f]{6}$/i.test(String(value || "")) ? value : fallback;
  }

  function normalizeSession(session) {
    if (!session || typeof session !== "object" || !String(session.id || "")) return null;
    const allowedRoles = new Set(["user", "assistant", "thinking", "tool", "compaction", "error"]);
    return {
      id: String(session.id),
      name: String(session.name || "Demo session"),
      project: String(session.project || "project"),
      monogram: String(session.monogram || "PR").slice(0, 3),
      color: safeIdentityColor(session.color, "#ff9b73"),
      background: safeIdentityColor(session.background, "#4a281f"),
      age: String(session.age || "Recently"),
      pinned: !!session.pinned,
      messages: Array.isArray(session.messages) ? session.messages.filter((message) => allowedRoles.has(message?.role)).map((message) => ({
        role: message.role,
        text: String(message.text || ""),
        title: String(message.title || ""),
        time: String(message.time || ""),
        code: String(message.code || ""),
        link: message.link?.href === "https://github.com/melounvitek/gripi" ? { href: message.link.href, label: String(message.link.label || "View GRIPi on GitHub →") } : null
      })) : []
    };
  }

  function jumpControlVisibility(previous, current, maximum) {
    if (current === previous) return { top: false, bottom: false };
    const direction = current < previous ? "up" : "down";
    const nearTop = current < 120;
    const nearBottom = maximum - current < 120;
    return { top: direction === "up" && !nearTop, bottom: direction === "down" && !nearBottom };
  }

  function responseScript(prompt) {
    const safePrompt = String(prompt || "your question").trim() || "your question";
    const answer = `This is a prerecorded response to “${safePrompt}”. In a real GRIPi session, Pi would now continue with full access to the selected project and its tools.\n\nThe static demo still mirrors the experience: messages appear live, tool activity is visible, and you can stop a response while it is streaming.`;
    const words = answer.match(/\S+\s*/g) || [answer];
    return [
      { type: "status", text: "Pi is thinking…", delay: 180 },
      { type: "thinking", text: "I’ll inspect the request and prepare a concise response.", delay: 500 },
      { type: "tool_start", title: "read demo/project-context.md", delay: 350 },
      { type: "tool_end", text: "Loaded representative project context for the interactive demo.", delay: 650 },
      { type: "assistant_start", delay: 280 },
      ...words.map((text) => ({ type: "delta", text, delay: 42 + Math.floor(Math.random() * 45) })),
      { type: "done", delay: 120 }
    ];
  }

  async function playScript(script, options) {
    const settings = options || {};
    const wait = settings.wait || ((milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds)));
    const onEvent = settings.onEvent || (() => {});
    const signal = settings.signal;
    for (const event of script) {
      if (signal && signal.aborted) return false;
      await wait(event.delay || 0);
      if (signal && signal.aborted) return false;
      onEvent(event);
    }
    return true;
  }

  const defaultSessionId = "welcome";
  global.GripiDemo = { playScript, responseScript, safeIdentityColor, jumpControlVisibility, defaultSessionId, demoSessionCount: initialSessions.length, hasUnreadSessions: false };
  if (typeof document === "undefined") return;

  const storageKey = "gripi:static-demo:v4";
  let sessions = initialSessions;
  let currentId = defaultSessionId;
  let streamController = null;
  let streamingEntry = null;
  let activeToolEntry = null;
  let switching = false;
  let switchGeneration = 0;
  let previousModalFocus = null;
  let findMatches = [];
  let findIndex = -1;
  let lastScrollTop = 0;
  let scrollRevealTimer = null;
  let programmaticScroll = false;
  let programmaticScrollTimer = null;
  let autoScrollEnabled = true;

  try {
    const stored = JSON.parse(localStorage.getItem(storageKey));
    if (Array.isArray(stored?.sessions) && stored.sessions.length) {
      const normalized = stored.sessions.map(normalizeSession).filter(Boolean);
      if (normalized.length) sessions = normalized;
    }
    if (sessions.some((session) => session.id === stored?.currentId)) currentId = stored.currentId;
  } catch (_error) {}

  const element = {
    history: document.getElementById("history-output"), live: document.getElementById("live-output"), scroll: document.getElementById("conversation-scroll"),
    pinned: document.getElementById("pinned-sessions-list"), sessions: document.getElementById("sessions-list"), empty: document.getElementById("sidebar-empty"),
    project: document.getElementById("project-filter"), projectTrigger: document.getElementById("project-select-trigger"), projectList: document.getElementById("project-select-listbox"),
    searchForm: document.getElementById("sidebar-session-search"), search: document.querySelector('#sidebar-session-search input[type="search"]'), clearFilters: document.querySelector("[data-sidebar-filters-clear]"),
    headerName: document.querySelector(".session-header-name"), headerProject: document.querySelector(".session-header-project"), form: document.getElementById("prompt-form"), prompt: document.querySelector(".prompt-form textarea"),
    state: document.querySelector(".composer-state"), stop: document.getElementById("stop-button"), commands: document.getElementById("command-list"), notice: document.getElementById("demo-notice"), attachmentTray: document.querySelector(".attachment-tray"),
    jumpTop: document.querySelector(".jump-controls--top"), jumpFirst: document.querySelector(".jump-to-first"), jumpBottom: document.querySelector(".jump-controls--bottom"), jumpLatest: document.querySelector(".jump-to-latest")
  };

  function currentSession() { return sessions.find((session) => session.id === currentId) || sessions[0]; }
  function persist() {
    try { localStorage.setItem(storageKey, JSON.stringify({ sessions, currentId })); } catch (_error) {}
  }
  function draftKey(id = currentId) { return `${storageKey}:draft:${id}`; }
  function persistDraft(id = currentId) {
    try {
      if (element.prompt.value) localStorage.setItem(draftKey(id), element.prompt.value);
      else localStorage.removeItem(draftKey(id));
    } catch (_error) {}
  }
  function loadDraft() {
    try { element.prompt.value = localStorage.getItem(draftKey()) || ""; } catch (_error) { element.prompt.value = ""; }
    element.commands.classList.toggle("is-visible", element.prompt.value.startsWith("/"));
  }
  function timeLabel() { return new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" }).format(new Date()); }
  function applyIdentity(target, session) {
    target.style.setProperty("--project-identity-bg", safeIdentityColor(session.background, "#4a281f"));
    target.style.setProperty("--project-identity-fg", safeIdentityColor(session.color, "#ff9b73"));
  }

  function makeCopyButton() {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "copy-button";
    button.dataset.copyTarget = "message";
    button.textContent = "Copy";
    return button;
  }

  function messageArticle(message, live) {
    const article = document.createElement("article");
    const role = message.role;
    const roleKey = role === "thinking" || role === "tool" ? "assistant" : role === "compaction" ? "status" : role;
    article.className = `message message--${roleKey}${role === "thinking" ? " message--thinking" : ""}${role === "tool" ? " message--compact message--tool-call" : ""}${role === "compaction" ? " message--compact" : ""}${live ? " message--live" : ""}`;
    article.dataset.role = roleKey;
    const header = document.createElement("header");
    header.className = "message-header";
    const label = document.createElement("div");
    label.className = "role";
    label.textContent = role === "assistant" || role === "thinking" ? "pi" : role === "compaction" ? "status" : role;
    const meta = document.createElement("div");
    meta.className = "message-meta";
    meta.textContent = message.time || timeLabel();
    header.append(label, meta);
    if (role === "assistant") header.append(makeCopyButton());
    article.append(header);

    if (role === "tool") {
      const details = document.createElement("div");
      details.className = "message-details message-details--always-open";
      const summary = document.createElement("div");
      summary.className = "message-details-summary";
      const compact = document.createElement("span");
      compact.className = "compact-summary";
      compact.textContent = message.title || "Tool activity";
      summary.append(compact);
      const collapse = document.createElement("div");
      collapse.className = "tool-output-collapse";
      collapse.dataset.toolOutputCollapse = "";
      collapse.dataset.expanded = "false";
      const control = document.createElement("div");
      control.className = "tool-output-collapse-control";
      const count = document.createElement("span");
      count.className = "tool-output-hidden-count tool-output-hidden-count--desktop";
      count.textContent = "… (3 earlier lines)";
      const toggle = document.createElement("button");
      toggle.type = "button";
      toggle.className = "tool-output-toggle";
      toggle.dataset.toolOutputToggle = "";
      toggle.setAttribute("aria-expanded", "false");
      toggle.textContent = "Expand";
      control.append(count, toggle);
      const body = document.createElement("pre");
      body.className = "message-body";
      body.dataset.toolOutputBody = "";
      body.textContent = message.text || "Running…";
      collapse.append(control, body);
      details.append(summary, collapse);
      article.append(details);
      article.toolBody = body;
    } else if (role === "compaction") {
      const details = document.createElement("details");
      details.className = "message-details message-details--compaction";
      const summary = document.createElement("summary");
      summary.className = "message-details-summary compaction-details-summary";
      const text = document.createElement("span");
      text.className = "compact-summary";
      text.textContent = message.title || "Conversation compacted";
      const action = document.createElement("span");
      action.className = "compaction-details-action";
      summary.append(text, action);
      const body = document.createElement("pre");
      body.className = "message-body";
      body.textContent = message.text;
      details.append(summary, body);
      article.append(details);
    } else {
      const body = document.createElement(role === "assistant" || role === "thinking" ? "div" : "pre");
      body.className = `message-body${role === "assistant" || role === "thinking" ? " message-body--markdown" : ""}${role === "thinking" ? " message-body--thinking" : ""}`;
      if (role === "assistant" || role === "thinking") {
        String(message.text || "").split(/\n\n+/).forEach((paragraph) => { const p = document.createElement("p"); p.textContent = paragraph; body.append(p); });
        if (message.code) { const pre = document.createElement("pre"); const code = document.createElement("code"); code.textContent = message.code; pre.append(code); body.append(pre); }
        if (message.link) { const p = document.createElement("p"); const link = document.createElement("a"); link.href = message.link.href; link.textContent = message.link.label; p.append(link); body.append(p); }
      } else body.textContent = message.text || "";
      article.append(body);
      article.messageBody = body;
    }
    return article;
  }

  function sessionRow(session) {
    const wrapper = document.createElement("div");
    wrapper.className = `session-row${session.pinned ? " is-pinned" : ""}`;
    const link = document.createElement("a");
    link.href = `#${session.id}`;
    link.className = `session recent-session${session.id === currentId ? " selected" : ""}`;
    link.dataset.sessionId = session.id;
    const content = document.createElement("div");
    content.className = "session-content";
    const title = document.createElement("div"); title.className = "session-title"; title.textContent = session.name;
    const project = document.createElement("div"); project.className = "session-project"; applyIdentity(project, session);
    const monogram = document.createElement("span"); monogram.className = "session-project-monogram"; monogram.textContent = session.monogram;
    const projectLabel = document.createElement("span"); projectLabel.className = "session-project-label"; projectLabel.textContent = session.project;
    project.append(monogram, projectLabel);
    const meta = document.createElement("div"); meta.className = "session-meta"; meta.textContent = session.age;
    content.append(title, project, meta);
    const indicators = document.createElement("div"); indicators.className = "session-indicators";
    link.append(content, indicators);
    const pin = document.createElement("button");
    pin.type = "button"; pin.className = `session-pin-toggle${session.pinned ? " is-pinned" : ""}`; pin.dataset.pinId = session.id; pin.setAttribute("aria-pressed", String(!!session.pinned)); pin.title = session.pinned ? "Unpin session" : "Pin session";
    pin.innerHTML = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M9 3h6l-1 5 3 3v2h-4l-1 8-1-8H7v-2l3-3-1-5Z"/></svg>';
    wrapper.append(link, pin);
    return wrapper;
  }

  function filteredSessions() {
    const query = element.search.value.trim().toLowerCase();
    return sessions.filter((session) => (!element.project.value || session.project === element.project.value) && (!query || `${session.name} ${session.project}`.toLowerCase().includes(query)));
  }

  function renderSidebar() {
    element.pinned.replaceChildren(); element.sessions.replaceChildren();
    const visible = filteredSessions();
    visible.filter((session) => session.pinned).forEach((session) => element.pinned.append(sessionRow(session)));
    visible.filter((session) => !session.pinned).forEach((session) => element.sessions.append(sessionRow(session)));
    element.empty.hidden = visible.length > 0;
    element.clearFilters.hidden = !element.project.value && !element.search.value;
  }

  function setJumpControls(top, bottom) {
    element.jumpTop.classList.toggle("is-visible", top);
    element.jumpFirst.classList.toggle("is-visible", top);
    element.jumpBottom.classList.toggle("is-visible", bottom);
    element.jumpLatest.classList.toggle("is-visible", bottom);
  }

  function renderConversation() {
    element.history.replaceChildren(); element.live.replaceChildren();
    currentSession().messages.forEach((message) => element.history.append(messageArticle(message, false)));
    requestAnimationFrame(() => {
      element.scroll.scrollTop = element.scroll.scrollHeight;
      lastScrollTop = element.scroll.scrollTop;
      autoScrollEnabled = true;
      setJumpControls(false, false);
    });
  }

  function renderHeader() {
    const session = currentSession();
    element.headerName.textContent = session.name;
    applyIdentity(element.headerProject, session);
    element.headerProject.querySelector(".session-header-project-icon").textContent = session.monogram;
    element.headerProject.querySelector(".session-header-project-label").textContent = session.project;
    document.title = `${session.name} · GRIPi demo`;
  }

  function setRunning(running, text) {
    element.state.dataset.state = running ? "running" : "idle";
    element.state.textContent = text || "";
    element.stop.hidden = !running;
    element.stop.disabled = !running;
    element.stop.classList.toggle("is-visible", running);
  }

  function finishProgrammaticScrollSoon() {
    clearTimeout(programmaticScrollTimer);
    programmaticScrollTimer = setTimeout(() => { programmaticScroll = false; lastScrollTop = element.scroll.scrollTop; }, 140);
  }
  function cancelProgrammaticScroll() {
    clearTimeout(programmaticScrollTimer);
    programmaticScroll = false;
    autoScrollEnabled = false;
    lastScrollTop = element.scroll.scrollTop;
  }
  function programmaticScrollTo(options) {
    programmaticScroll = true;
    setJumpControls(false, false);
    element.scroll.scrollTo(options);
    finishProgrammaticScrollSoon();
  }
  function programmaticScrollIntoView(target) {
    programmaticScroll = true;
    setJumpControls(false, false);
    target.scrollIntoView({ block: "center" });
    finishProgrammaticScrollSoon();
  }
  function scrollLatest(force = false) {
    if (!autoScrollEnabled && !force) return;
    if (force) autoScrollEnabled = true;
    programmaticScrollTo({ top: element.scroll.scrollHeight, behavior: "smooth" });
  }
  function showDemoNotice(text) { element.notice.querySelector("[data-demo-notice-message]").textContent = text; element.notice.classList.add("is-visible"); }

  function cancelStream(feedback) {
    if (!streamController) return;
    streamController.abort(); streamController = null;
    streamingEntry?.article.classList.remove("message--streaming");
    if (streamingEntry && !streamingEntry.message.text) {
      streamingEntry.session.messages = streamingEntry.session.messages.filter((message) => message !== streamingEntry.message);
      streamingEntry.article.remove();
    }
    if (activeToolEntry) {
      activeToolEntry.article.classList.remove("message--live");
      if (activeToolEntry.message.text === "Running…") {
        activeToolEntry.message.text = "Stopped before the simulated tool completed.";
        activeToolEntry.body.textContent = activeToolEntry.message.text;
      }
    }
    streamingEntry = null; activeToolEntry = null;
    setRunning(false);
    if (feedback) showDemoNotice("Simulated response stopped. No backend request was made.");
    persist();
  }

  function appendStreamEvent(event, session) {
    if (event.type === "status") setRunning(true, event.text);
    if (event.type === "thinking") {
      const message = { role: "thinking", text: event.text, time: timeLabel() }; session.messages.push(message); element.live.append(messageArticle(message, true));
    }
    if (event.type === "tool_start") {
      const message = { role: "tool", title: event.title, text: "Running…", time: timeLabel() }; session.messages.push(message);
      const article = messageArticle(message, true); article.classList.add("message--live"); element.live.append(article); activeToolEntry = { article, body: article.toolBody, message }; setRunning(true, "Using tools…");
    }
    if (event.type === "tool_end" && activeToolEntry) { activeToolEntry.message.text = event.text; activeToolEntry.body.textContent = event.text; activeToolEntry.article.classList.remove("message--live"); setRunning(true, "Pi is responding…"); }
    if (event.type === "assistant_start") {
      const message = { role: "assistant", text: "", time: timeLabel() }; session.messages.push(message);
      const article = messageArticle(message, true); article.classList.add("message--streaming"); element.live.append(article);
      streamingEntry = { article, body: article.messageBody, message, session };
    }
    if (event.type === "delta" && streamingEntry) {
      streamingEntry.message.text += event.text;
      let paragraph = streamingEntry.body.lastElementChild;
      if (!paragraph) { paragraph = document.createElement("p"); streamingEntry.body.append(paragraph); }
      paragraph.textContent += event.text;
    }
    if (event.type === "done") {
      streamingEntry?.article.classList.remove("message--streaming"); streamController = null; streamingEntry = null; activeToolEntry = null; setRunning(false); persist();
    }
    scrollLatest();
  }

  async function submitPrompt() {
    const prompt = element.prompt.value.trim();
    if (!prompt || streamController || switching) return;
    if (prompt.startsWith("/")) { handleSlash(prompt.split(/\s/)[0].slice(1)); element.prompt.value = ""; persistDraft(); element.commands.classList.remove("is-visible"); return; }
    const streamSession = currentSession();
    const message = { role: "user", text: prompt, time: timeLabel() };
    streamSession.messages.push(message); element.live.append(messageArticle(message, true)); element.prompt.value = ""; persistDraft();
    element.attachmentTray.replaceChildren(); element.attachmentTray.classList.remove("has-attachments"); document.getElementById("image-input").value = "";
    setRunning(true, "Sending…"); persist(); scrollLatest(true);
    streamController = new AbortController();
    await playScript(responseScript(prompt), { signal: streamController.signal, onEvent: (event) => appendStreamEvent(event, streamSession) });
  }

  function openModal(name) {
    const modal = document.querySelector(`[data-modal="${name}"]`); if (!modal) return;
    previousModalFocus = document.activeElement; modal.hidden = false;
    (modal.querySelector("[data-modal-default-focus]") || modal.querySelector("button, input, select"))?.focus();
  }
  function closeModal(modal) { if (!modal) return; modal.hidden = true; previousModalFocus?.focus(); previousModalFocus = null; }
  function handleSlash(command) {
    const modals = { new: "new-session-modal", fork: "fork-session-modal", tree: "tree-session-modal", model: "model-settings-modal" };
    if (modals[command]) openModal(modals[command]);
    else if (command === "compact") { const message = { role: "compaction", title: "Conversation compacted", text: "Static demo compaction summary. In production this is generated by Pi.", time: timeLabel() }; currentSession().messages.push(message); element.live.append(messageArticle(message, true)); persist(); scrollLatest(true); }
  }

  function switchSession(id) {
    if (!sessions.some((session) => session.id === id)) return;
    const generation = ++switchGeneration;
    switching = true;
    element.prompt.disabled = true;
    persistDraft(); cancelStream(false); resetFind(true); document.body.classList.add("session-switching");
    setTimeout(() => {
      if (generation !== switchGeneration) return;
      currentId = id;
      const session = currentSession();
      renderHeader(); renderConversation(); renderSidebar(); loadDraft(); persist();
      switching = false;
      element.prompt.disabled = false;
      document.body.classList.remove("session-switching");
      document.getElementById("mobile-session-toggle").checked = false;
    }, 220);
  }

  function clearFindMatches() {
    document.querySelectorAll(".current-session-find-match").forEach((match) => { const parent = match.parentNode; match.replaceWith(document.createTextNode(match.textContent)); parent.normalize(); });
  }

  function resetFind(hide = false) {
    clearFindMatches(); findMatches = []; findIndex = -1;
    const container = document.querySelector("[data-current-session-find]");
    container.querySelector("input").value = "";
    container.querySelector("output").textContent = "0 / 0";
    if (hide) container.hidden = true;
  }

  function updateFind() {
    clearFindMatches();
    const query = document.querySelector("[data-current-session-find-input]").value.trim();
    const conversationOnly = document.querySelector("[data-current-session-find-conversation-only]").checked;
    findMatches = [];
    if (query) {
      const selector = conversationOnly ? ".message-body" : ".message";
      element.scroll.querySelectorAll(selector).forEach((root) => {
        const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
        const nodes = [];
        while (walker.nextNode()) if (!walker.currentNode.parentElement.closest("button, summary")) nodes.push(walker.currentNode);
        nodes.forEach((node) => {
          const lower = node.data.toLowerCase();
          const needle = query.toLowerCase();
          let start = 0;
          if (!lower.includes(needle)) return;
          const fragment = document.createDocumentFragment();
          while (start < node.data.length) {
            const index = lower.indexOf(needle, start);
            if (index < 0) { fragment.append(node.data.slice(start)); break; }
            fragment.append(node.data.slice(start, index));
            const match = document.createElement("mark"); match.className = "current-session-find-match"; match.textContent = node.data.slice(index, index + query.length); fragment.append(match); findMatches.push(match);
            start = index + query.length;
          }
          node.replaceWith(fragment);
        });
      });
    }
    findIndex = findMatches.length ? Math.min(Math.max(findIndex, 0), findMatches.length - 1) : -1;
    findMatches.forEach((match, index) => match.classList.toggle("is-active", index === findIndex));
    document.querySelector("[data-current-session-find-count]").textContent = findMatches.length ? `${findIndex + 1} / ${findMatches.length}` : "0 / 0";
    if (findMatches[findIndex]) programmaticScrollIntoView(findMatches[findIndex]);
  }
  function moveFind(direction) { if (!findMatches.length) return; findIndex = (findIndex + direction + findMatches.length) % findMatches.length; updateFind(); }

  document.addEventListener("click", (event) => {
    const sessionLink = event.target.closest("[data-session-id]"); if (sessionLink) { event.preventDefault(); switchSession(sessionLink.dataset.sessionId); return; }
    const pin = event.target.closest("[data-pin-id]"); if (pin) { const session = sessions.find((item) => item.id === pin.dataset.pinId); session.pinned = !session.pinned; persist(); renderSidebar(); return; }
    const open = event.target.closest("[data-modal-open]"); if (open) { openModal(open.dataset.modalOpen); return; }
    const close = event.target.closest("[data-modal-close]"); if (close) { closeModal(close.closest("[data-modal]")); return; }
    const command = event.target.closest("[data-command-name]"); if (command) { handleSlash(command.dataset.commandName); element.prompt.value = ""; persistDraft(); element.commands.classList.remove("is-visible"); return; }
    const toggle = event.target.closest("[data-tool-output-toggle]"); if (toggle) { const collapse = toggle.closest("[data-tool-output-collapse]"); const expanded = collapse.dataset.expanded !== "true"; collapse.dataset.expanded = String(expanded); toggle.textContent = expanded ? "Collapse" : "Expand"; toggle.setAttribute("aria-expanded", String(expanded)); return; }
    const copy = event.target.closest("[data-copy-target]"); if (copy) {
      const text = copy.closest(".message").querySelector(".message-body")?.textContent || "";
      const fallback = () => { const textarea = document.createElement("textarea"); textarea.value = text; textarea.className = "visually-hidden"; document.body.append(textarea); textarea.select(); const copied = document.execCommand("copy"); textarea.remove(); return copied; };
      Promise.resolve(navigator.clipboard?.writeText ? navigator.clipboard.writeText(text).then(() => true).catch(fallback) : fallback()).then((copied) => { copy.textContent = copied ? "Copied" : "Copy failed"; setTimeout(() => { copy.textContent = "Copy"; }, 1200); }); return;
    }
    const removeAttachment = event.target.closest("[data-remove-attachment]"); if (removeAttachment) { removeAttachment.closest(".attachment")?.remove(); element.attachmentTray.classList.toggle("has-attachments", !!element.attachmentTray.children.length); document.getElementById("image-input").value = ""; return; }
    if (!event.target.closest("[data-project-select]") && !element.projectList.hidden) { element.projectList.hidden = true; element.projectTrigger.setAttribute("aria-expanded", "false"); }
    if (event.target.closest("[data-demo-notice]")) { event.preventDefault(); showDemoNotice("This control needs a connected gateway. The static demo stays on this page."); }
    if (event.target.closest("[data-dismiss-notice]")) element.notice.classList.remove("is-visible");
    if (event.target.closest(".jump-to-first")) { autoScrollEnabled = false; programmaticScrollTo({ top: 0, behavior: "smooth" }); }
    if (event.target.closest(".jump-to-latest")) scrollLatest(true);
  });

  element.scroll.addEventListener("scroll", () => {
    const current = element.scroll.scrollTop;
    if (programmaticScroll) { lastScrollTop = current; setJumpControls(false, false); finishProgrammaticScrollSoon(); return; }
    const maximum = Math.max(0, element.scroll.scrollHeight - element.scroll.clientHeight);
    autoScrollEnabled = maximum - current < 120;
    const visible = jumpControlVisibility(lastScrollTop, current, maximum);
    lastScrollTop = current;
    setJumpControls(visible.top, visible.bottom);
    document.body.classList.add("is-conversation-scrolling");
    clearTimeout(scrollRevealTimer);
    scrollRevealTimer = setTimeout(() => document.body.classList.remove("is-conversation-scrolling"), 1400);
  }, { passive: true });
  ["wheel", "touchstart", "pointerdown", "keydown"].forEach((type) => element.scroll.addEventListener(type, cancelProgrammaticScroll, { passive: true }));

  document.querySelector("[data-sidebar-search-toggle]").addEventListener("click", (event) => { const open = !element.searchForm.classList.contains("is-open"); element.searchForm.classList.toggle("is-open", open); event.currentTarget.classList.toggle("is-active", open); event.currentTarget.setAttribute("aria-expanded", String(open)); if (open) element.search.focus(); });
  element.search.addEventListener("input", renderSidebar);
  function selectProject(value) {
    const option = element.projectList.querySelector(`[data-project-value="${value}"]`) || element.projectList.querySelector('[data-project-value=""]');
    element.project.value = option.dataset.projectValue;
    element.projectTrigger.querySelector(":scope > :first-child").replaceWith(option.firstElementChild.cloneNode(true));
    element.projectTrigger.querySelector(".project-select-trigger-label").textContent = option.querySelector(".project-select-option-label").textContent;
    element.projectList.querySelectorAll("[role=option]").forEach((item) => { item.classList.toggle("is-active", item === option); item.setAttribute("aria-selected", String(item === option)); });
    element.projectList.hidden = true; element.projectTrigger.setAttribute("aria-expanded", "false"); renderSidebar();
  }
  element.clearFilters.addEventListener("click", (event) => { event.preventDefault(); element.search.value = ""; selectProject(""); });
  element.projectTrigger.addEventListener("click", () => { const open = element.projectList.hidden; element.projectList.hidden = !open; element.projectTrigger.setAttribute("aria-expanded", String(open)); if (open) { const rect = element.projectTrigger.getBoundingClientRect(); Object.assign(element.projectList.style, { left: `${rect.left}px`, top: `${rect.bottom + 4}px`, width: `${rect.width}px` }); } });
  element.projectList.addEventListener("click", (event) => { const option = event.target.closest("[data-project-value]"); if (option) selectProject(option.dataset.projectValue); });
  document.querySelector("[data-notification-toggle]").addEventListener("click", (event) => { const enabled = !event.currentTarget.classList.contains("is-enabled"); event.currentTarget.classList.toggle("is-enabled", enabled); event.currentTarget.classList.toggle("is-disabled", !enabled); event.currentTarget.querySelector("[data-notification-toggle-state]").textContent = enabled ? "Demo on" : "Demo off"; showDemoNotice(enabled ? "Demo notifications enabled. No browser permission or network service is used." : "Demo notifications disabled."); });
  element.form.addEventListener("submit", (event) => { event.preventDefault(); submitPrompt(); });
  element.prompt.addEventListener("input", () => { const slash = element.prompt.value.startsWith("/"); element.commands.classList.toggle("is-visible", slash); if (slash) element.commands.open = true; persistDraft(); });
  element.prompt.addEventListener("keydown", (event) => { if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); submitPrompt(); } });
  element.stop.addEventListener("click", () => cancelStream(true));
  document.getElementById("image-input").addEventListener("change", (event) => { element.attachmentTray.replaceChildren(); [...event.target.files].forEach((file) => { const attachment = document.createElement("span"); attachment.className = "attachment"; const name = document.createElement("span"); name.textContent = file.name; const remove = document.createElement("button"); remove.type = "button"; remove.dataset.removeAttachment = ""; remove.textContent = "Remove"; attachment.append(name, remove); element.attachmentTray.append(attachment); }); element.attachmentTray.classList.toggle("has-attachments", event.target.files.length > 0); });

  document.querySelector("[data-current-session-find-input]").addEventListener("input", updateFind);
  document.querySelector("[data-current-session-find-conversation-only]").addEventListener("change", updateFind);
  document.querySelector("[data-current-session-find-previous]").addEventListener("click", () => moveFind(-1));
  document.querySelector("[data-current-session-find-next]").addEventListener("click", () => moveFind(1));
  document.querySelector("[data-current-session-find-close]").addEventListener("click", () => resetFind(true));
  document.addEventListener("keydown", (event) => {
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "f") { event.preventDefault(); const find = document.querySelector("[data-current-session-find]"); find.hidden = false; find.querySelector("input").focus(); }
    if (event.key === "Escape") { const modal = document.querySelector("[data-modal]:not([hidden])"); if (modal) closeModal(modal); else if (!element.projectList.hidden) { element.projectList.hidden = true; element.projectTrigger.setAttribute("aria-expanded", "false"); element.projectTrigger.focus(); } }
    if (event.key === "Tab") { const modal = document.querySelector("[data-modal]:not([hidden])"); if (!modal) return; const focusable = [...modal.querySelectorAll("button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled])")]; if (!focusable.length) return; const first = focusable[0], last = focusable[focusable.length - 1]; if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); } else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); } }
  });

  const newSessionSelect = document.querySelector("[data-new-session-known-cwd]");
  const newSessionTrigger = document.querySelector("[data-new-session-project-trigger]");
  const newSessionList = document.querySelector("[data-new-session-project-list]");
  const newSessionProjectFields = document.querySelector("[data-new-session-project-fields]");
  const newSessionPathFields = document.querySelector("[data-new-session-path-fields]");
  const newSessionPathInput = document.querySelector("[data-new-session-cwd-input]");
  const newSessionCwd = document.querySelector("[data-new-session-cwd-value]");
  const newSessionMessage = document.querySelector("[data-new-session-cwd-message]");

  newSessionTrigger.addEventListener("click", () => {
    const open = newSessionList.hidden;
    newSessionList.hidden = !open;
    newSessionTrigger.setAttribute("aria-expanded", String(open));
    if (open) {
      const rect = newSessionTrigger.getBoundingClientRect();
      Object.assign(newSessionList.style, { left: `${rect.left}px`, top: `${rect.bottom + 4}px`, width: `${rect.width}px` });
    }
  });
  newSessionList.addEventListener("click", (event) => {
    const option = event.target.closest("[data-new-project]");
    if (!option) return;
    const value = option.dataset.newProject;
    newSessionSelect.value = value;
    newSessionList.querySelectorAll("[role=option]").forEach((item) => { item.classList.toggle("is-active", item === option); item.setAttribute("aria-selected", String(item === option)); });
    newSessionList.hidden = true;
    newSessionTrigger.setAttribute("aria-expanded", "false");
    if (value === "__new_path__") {
      newSessionProjectFields.hidden = true;
      newSessionPathFields.hidden = false;
      newSessionMessage.textContent = "Enter an existing directory.";
      newSessionPathInput.focus();
    } else {
      newSessionTrigger.querySelector(":scope > :first-child").replaceWith(option.firstElementChild.cloneNode(true));
      newSessionTrigger.querySelector(".project-select-trigger-label").textContent = option.querySelector(".project-select-option-label").textContent;
      newSessionCwd.value = `/home/demo/Work/${value}`;
    }
  });
  document.querySelector("[data-new-session-project-mode]").addEventListener("click", () => { newSessionPathFields.hidden = true; newSessionProjectFields.hidden = false; newSessionSelect.value = "gripi"; newSessionCwd.value = "/home/demo/Work/gripi"; newSessionMessage.textContent = "The static demo creates a representative local session."; newSessionTrigger.focus(); });
  newSessionPathInput.addEventListener("input", () => { newSessionCwd.value = newSessionPathInput.value.trim(); const valid = newSessionCwd.value.startsWith("/"); newSessionMessage.textContent = valid ? "Directory available in the static demo." : "Enter an absolute directory path."; newSessionMessage.classList.toggle("is-valid", valid); newSessionMessage.classList.toggle("is-invalid", !!newSessionCwd.value && !valid); });

  document.querySelector(".new-session-cwd-form").addEventListener("submit", (event) => {
    event.preventDefault();
    const identities = {
      gripi: { monogram: "GR", color: "#ff9b73", background: "#4a281f" },
      website: { monogram: "WE", color: "#9fc5ff", background: "#1e334d" },
      storefront: { monogram: "ST", color: "#b5e3b0", background: "#203c2b" }
    };
    const customMode = new FormData(event.target).get("known_cwd") === "__new_path__";
    const cwd = newSessionCwd.value.trim();
    if (!cwd.startsWith("/")) { newSessionMessage.textContent = "Enter an absolute directory path."; newSessionMessage.classList.add("is-invalid"); if (customMode) newSessionPathInput.focus(); return; }
    const project = cwd.split("/").filter(Boolean).pop() || "project";
    const identity = identities[project] || { monogram: project.slice(0, 2).toUpperCase(), color: "#f0c674", background: "#3c3728" };
    const id = `local-${Date.now()}`;
    sessions.unshift({ id, name: "New local demo session", project, ...identity, age: "Just now", pinned: false, messages: [{ role: "assistant", text: "This representative session was created locally. Enter a prompt to try streaming.", time: timeLabel() }] });
    closeModal(event.target.closest("[data-modal]")); switchSession(id);
  });
  document.querySelectorAll("[data-demo-fork]").forEach((button) => button.addEventListener("click", () => { const source = currentSession(); const id = `fork-${Date.now()}`; sessions.push({ ...source, id, name: `${source.name} (fork)`, pinned: false, messages: source.messages.slice(0, 4).map((message) => ({ ...message })) }); closeModal(button.closest("[data-modal]")); switchSession(id); }));
  document.querySelectorAll("[data-demo-tree]").forEach((button, index) => button.addEventListener("click", () => { closeModal(button.closest("[data-modal]")); if (index === 0) switchSession("api-errors"); else showDemoNotice("Already viewing this tree point."); }));
  document.querySelector(".model-settings-form").addEventListener("submit", (event) => { event.preventDefault(); const model = new FormData(event.target).get("model"); const thinking = new FormData(event.target).get("thinking"); document.querySelector('[data-status-key="model"] .session-status-value').textContent = `${model} (${thinking})`; closeModal(event.target.closest("[data-modal]")); showDemoNotice("Model settings applied locally for the demo."); });
  document.querySelector("[data-model-search]").addEventListener("input", (event) => { const query = event.target.value.toLowerCase(); document.querySelectorAll(".model-option").forEach((option) => { option.hidden = !option.textContent.toLowerCase().includes(query); }); });

  renderHeader(); renderConversation(); renderSidebar(); loadDraft();
})(globalThis);
