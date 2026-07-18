(function (global) {
  "use strict";

  const initialSessions = [
    {
      id: "welcome", name: "Welcome to Gripi", project: "gripi", monogram: "GR", color: "#ff9b73", background: "#4a281f", age: "just now", pinned: true,
      messages: [
        { role: "user", text: "What is Gripi, and how can I get started?" },
        { role: "thinking", text: "I’ll give you a quick tour and the shortest path to a local installation." },
        { role: "assistant", text: "Gripi is a desktop and web portal for Pi, powered by a self-hosted gateway. Here, Pi means the coding-agent harness that connects the model to files, shell commands, tools, edits, and session history — not Raspberry Pi hardware. Run the gateway on your development machine or home server, then use your existing Pi projects and sessions from the desktop app or a browser.\n\nPi stays Pi: Gripi does not alter Pi’s system prompt, patch Pi, install extensions, rewrite sessions, or change Pi-owned configuration. This static demo lets you explore session navigation, settings, streamed responses, and tool activity. Prompts stay in this browser, and all Pi or gateway behavior is simulated." },
        { role: "assistant", text: "Clone and start Gripi with these commands. Setup prints the admin password used to approve your browser, then Gripi is available at http://localhost:4567.", code: "git clone https://github.com/melounvitek/gripi.git\ncd gripi\nmise install\nmise run setup\nGRIPI_HOST=127.0.0.1 mise run start", link: { href: "https://github.com/melounvitek/gripi", label: "View Gripi on GitHub →" } }
      ]
    },
    {
      id: "new-to-pi", name: "New to Pi? Start here", project: "gripi", monogram: "GR", color: "#ff9b73", background: "#4a281f", age: "2 minutes ago", pinned: false,
      messages: [
        { role: "user", text: "I found Gripi before Pi. Is this a good place to start?" },
        { role: "assistant", text: "Start with Pi itself. Pi is the coding-agent harness that reads files, runs tools, edits code, and owns the projects and sessions shown here — not Raspberry Pi hardware. Gripi runs that same Pi environment on a gateway machine and gives you desktop and browser access to it.\n\nTry Pi CLI first and become comfortable with its tools, sessions, models, extensions, and filesystem access. Gripi becomes useful when you want to reach an existing Pi setup from other devices.", link: { href: "https://pi.dev/", label: "Learn about Pi →" } }
      ]
    },
    {
      id: "pi-stays-pi", name: "Does Gripi change Pi?", project: "gripi", monogram: "GR", color: "#ff9b73", background: "#4a281f", age: "5 minutes ago", pinned: false,
      messages: [
        { role: "user", text: "Does Gripi change how Pi behaves?" },
        { role: "assistant", text: "No. Gripi does not alter Pi’s system prompt, patch Pi, install extensions, rewrite sessions, or change Pi-owned configuration. It starts and connects to the Pi coding-agent harness through a gateway, while Pi remains responsible for the actual agent runtime, tools, models, and session data." }
      ]
    },
    {
      id: "unsupported", name: "What isn’t supported in Gripi?", project: "gripi", monogram: "GR", color: "#ff9b73", background: "#4a281f", age: "9 minutes ago", pinned: false,
      messages: [
        { role: "user", text: "Does Gripi support everything an extension can do in Pi’s terminal UI?" },
        { role: "assistant", text: "No. Standard tools, compatible custom tools, subagents, extension commands exposed through RPC, semantic extension dialogs, session data, images, compaction, and tree navigation work through Pi’s gateway runtime.\n\nGripi supports RPC-compatible extension UI such as select, confirm, input, editor, notify, status, title, and editor-prefill requests. It does not reproduce arbitrary terminal interfaces: Custom TUI components, overlays, widgets backed by terminal rendering, terminal-only editors, terminal keybindings, and custom TUI rendering are not currently supported." },
        { role: "assistant", text: "Use Pi CLI directly for workflows that depend on custom terminal UI or explicitly require ctx.mode === “tui”. Gripi preserves the underlying Pi workflow, but it is not a browser implementation of every possible extension interface." }
      ]
    },
    {
      id: "always-on", name: "Run Gripi on an always-on computer", project: "gripi", monogram: "GR", color: "#ff9b73", background: "#4a281f", age: "14 minutes ago", pinned: false,
      messages: [
        { role: "user", text: "I have an office PC or spare computer that stays on. Can it be my gateway?" },
        { role: "thinking", text: "I’ll outline the reliable private-network setup and the machine-access implications." },
        { role: "assistant", text: "Yes. Install Pi and Gripi there and connect it through Tailscale. On Linux, you can optionally run Gripi as a user systemd service and enable user lingering if it must start before login. Configure the computer not to sleep unexpectedly.\n\nCheck your employer’s policy before using an office machine. Anyone allowed into Gripi can ask Pi to access that computer’s projects and credentials.", code: "systemctl --user enable --now gripi.service\nsudo loginctl enable-linger \"$USER\"\nsystemctl --user status gripi.service --no-pager", link: { href: "https://github.com/melounvitek/gripi/blob/master/docs/examples.md", label: "Read the always-on setup guide →" } }
      ]
    },
    {
      id: "tailscale", name: "Access Gripi remotely with Tailscale", project: "gripi", monogram: "GR", color: "#ff9b73", background: "#4a281f", age: "22 minutes ago", pinned: false,
      messages: [
        { role: "user", text: "What is the recommended way to connect from another device?" },
        { role: "assistant", text: "Use a private VPN such as Tailscale. Either bind Gripi to the gateway’s Tailscale address, or keep it on 127.0.0.1 and use Tailscale Serve for private HTTPS. Keep browser approval enabled.\n\nDo not expose port 4567 directly to the public internet.", code: "GRIPI_HOST=127.0.0.1 mise run start\ntailscale serve --bg --yes 4567\ntailscale serve status", link: { href: "https://github.com/melounvitek/gripi/blob/master/docs/examples.md", label: "See the remote-access options →" } }
      ]
    },
    {
      id: "mobile", name: "Use Gripi from a phone or tablet", project: "gripi", monogram: "GR", color: "#ff9b73", background: "#4a281f", age: "31 minutes ago", pinned: false,
      messages: [
        { role: "user", text: "Can I use Gripi from my phone?" },
        { role: "assistant", text: "Yes. Join the phone to the same Tailscale network, open the gateway’s private URL, and approve that browser. On iPhone, Safari’s Add to Home Screen flow can open Gripi as a web app.\n\nThere is no native mobile app, but the web interface includes a responsive session sidebar and composer.", link: { href: "https://github.com/melounvitek/gripi/blob/master/docs/examples.md", label: "Review the private-network setup →" } }
      ]
    },
    {
      id: "vps", name: "Should I run Gripi on a VPS?", project: "gripi", monogram: "GR", color: "#ff9b73", background: "#4a281f", age: "45 minutes ago", pinned: false,
      messages: [
        { role: "user", text: "Could I deploy Gripi on an internet-hosted VPS?" },
        { role: "thinking", text: "This needs a strong warning because the gateway can execute Pi tools with the server user’s access." },
        { role: "assistant", text: "Running Gripi on a VPS is advanced and potentially dangerous. Do not do it unless you understand server hardening, network isolation, credential management, and remote-code-execution risk. Never expose Gripi through a public IP or public reverse proxy. Browser approval is not a substitute for network isolation." },
        { role: "tool", title: "Safer deployment checklist", text: "Private VPN only\nDedicated non-root OS user\nMinimal repositories and credentials\nFirewall and security updates\nEncrypted storage, backups, and monitoring\nBrowser approval kept enabled" },
        { role: "assistant", text: "Prefer an always-on trusted computer or private home server. If you still choose a VPS, expose it only inside a private VPN such as Tailscale and treat every credential available to that OS user as reachable through Pi.", link: { href: "https://github.com/melounvitek/gripi/blob/master/docs/configuration.md", label: "Review Gripi security configuration →" } }
      ]
    },
    {
      id: "demo-fidelity", name: "Does this look 1:1 realistic as the real product?", project: "gripi", monogram: "GR", color: "#ff9b73", background: "#4a281f", age: "1 hour ago", pinned: false,
      messages: [
        { role: "user", text: "Does this static demo look exactly like the real product?" },
        { role: "assistant", text: "Not exactly. The demo uses the same overall interface, but some interactions and details are simplified, and a few parts are not as polished here as they are in the real app. A connected Gripi gateway has the full live session behavior, real tool output, and production rendering polish." }
      ]
    },
    { id: "release-notes", name: "Draft release notes", project: "website", monogram: "WE", color: "#9fc5ff", background: "#1e334d", age: "2 hours ago", pinned: false, messages: [
      { role: "user", text: "Draft concise release notes from the changes in this branch.", time: "Monday" },
      { role: "thinking", text: "I’ll inspect the branch diff, then turn the user-facing parts into short release notes.", time: "Monday" },
      { role: "tool", title: "bash git status --short && git diff --stat origin/main...HEAD", text: " M app/components/sidebar/search.tsx\n M app/components/composer/index.tsx\n M app/lib/drafts.ts\n\n app/components/sidebar/search.tsx | 42 ++++++++++++++++++++++----\n app/components/composer/index.tsx | 18 +++++++----\n app/lib/drafts.ts                 | 27 +++++++++++++++--\n 3 files changed, 73 insertions(+), 14 deletions(-)", time: "Monday" },
      { role: "tool", title: "read app/components/sidebar/search.tsx", text: "export function SessionSearch({ sessions }) {\n  const [query, setQuery] = useState(\"\");\n  const visibleSessions = filterSessions(sessions, query);\n  return <SearchList sessions={visibleSessions} />;\n}", time: "Monday" },
      { role: "tool", title: "write content/app/releases.md", text: "Wrote release note draft with three bullets and a short upgrade note.", time: "Monday" },
      { role: "assistant", text: "Drafted concise release notes in `content/app/releases.md`.\n\nWhat’s new\n\n• Find sessions faster with sidebar search.\n• Enjoy a cleaner composer on mobile screens.\n• Keep unsent drafts when reconnecting.", time: "Monday" }
    ] },
    { id: "billing-spec", name: "Investigate flaky checkout spec", project: "storefront", monogram: "ST", color: "#b5e3b0", background: "#203c2b", age: "yesterday", pinned: false, messages: [
      { role: "user", text: "The checkout browser spec fails about one run in twenty. Can you investigate without weakening its assertions?", time: "Last week" },
      { role: "thinking", text: "I’ll reproduce it repeatedly and compare browser state on failed runs before changing the spec.", time: "Last week" },
      { role: "tool", title: "bash bin/rails test test/system/checkout_test.rb TESTOPTS='--seed 18422'", text: "Run options: --seed 18422\n\n# Running:\n\nF\n\nFailure:\nCheckoutTest#test_customer_can_pay_for_cart:\nExpected to find text \"Order confirmed\" in \"Redirecting…\"", time: "Last week" },
      { role: "tool", title: "read test/system/checkout_test.rb", text: "click_button \"Pay now\"\nassert_text \"Order confirmed\"\nassert_text order.number", time: "Last week" },
      { role: "tool", title: "edit test/system/checkout_test.rb", text: "- assert_text \"Order confirmed\"\n+ assert_current_path order_path(order), wait: 5\n+ assert_text \"Order confirmed\"", time: "Last week" },
      { role: "tool", title: "bash bin/rails test test/system/checkout_test.rb", text: "20 runs, 86 assertions, 0 failures, 0 errors, 0 skips", time: "Last week" },
      { role: "assistant", text: "The assertion sometimes ran before the payment redirect completed. I kept the same user-visible assertions and added an explicit wait for the order page navigation.\n\nFiles changed\n\n• `test/system/checkout_test.rb`", time: "Last week" }
    ] },
    { id: "docs-navigation", name: "Simplify documentation navigation", project: "website", monogram: "WE", color: "#9fc5ff", background: "#1e334d", age: "2026-06-17", pinned: false, messages: [
      { role: "user", text: "Can you reorganize the setup and configuration guides?", time: "Last month" },
      { role: "thinking", text: "I’ll map the current docs first so the new navigation follows existing content instead of inventing new sections.", time: "Last month" },
      { role: "tool", title: "bash find docs -maxdepth 2 -type f | sort", text: "docs/configuration.md\ndocs/deployment.md\ndocs/getting-started.md\ndocs/troubleshooting.md", time: "Last month" },
      { role: "tool", title: "read docs/getting-started.md", text: "# Getting started\n\nInstall dependencies, copy the example environment file, then start the local server.", time: "Last month" },
      { role: "tool", title: "write docs/setup.md", text: "Created a setup guide with sections for installation, local development, and common first-run problems.", time: "Last month" },
      { role: "tool", title: "edit docs/configuration.md", text: "- See deployment.md for hosting details.\n+ See setup.md for local setup and deployment.md for hosting details.", time: "Last month" },
      { role: "assistant", text: "Reorganized the guides around installation, local use, and remote access. I kept the existing content, moved setup steps into `docs/setup.md`, and added cross-links from configuration and deployment.", time: "Last month" }
    ] },
    { id: "checkout-copy", name: "Polish checkout confirmation copy", project: "storefront", monogram: "ST", color: "#b5e3b0", background: "#203c2b", age: "2026-06-17", pinned: false, messages: [
      { role: "user", text: "Make the confirmation screen clearer without adding more steps.", time: "Last month" },
      { role: "thinking", text: "I’ll keep the checkout flow unchanged and only adjust the confirmation page copy/layout.", time: "Last month" },
      { role: "tool", title: "read app/views/checkouts/show.html.erb", text: "<h1>Thank you for completing checkout</h1>\n<p>Your order is being prepared.</p>\n<p>Order <%= @order.number %></p>", time: "Last month" },
      { role: "tool", title: "edit app/views/checkouts/show.html.erb", text: "- <h1>Thank you for completing checkout</h1>\n+ <h1>Your order is confirmed</h1>\n  <p>Your order is being prepared.</p>\n- <p>Order <%= @order.number %></p>\n+ <p>Order <%= @order.number %> · Arrives <%= @order.delivery_window %></p>", time: "Last month" },
      { role: "tool", title: "bash bin/rails test test/system/checkout_test.rb", text: "20 runs, 89 assertions, 0 failures, 0 errors, 0 skips", time: "Last month" },
      { role: "assistant", text: "Shortened the heading and surfaced the delivery estimate next to the order number. No checkout steps changed.\n\nFiles changed\n\n• `app/views/checkouts/show.html.erb`", time: "Last month" }
    ] },
    { id: "ci-cache", name: "Speed up CI dependency caching", project: "storefront", monogram: "ST", color: "#b5e3b0", background: "#203c2b", age: "2026-05-17", pinned: false, messages: [
      { role: "user", text: "The test workflow spends too long installing unchanged dependencies.", time: "2 months ago" },
      { role: "thinking", text: "I’ll check which dependency managers are used and keep the cache key tied to lockfiles, not unrelated source changes.", time: "2 months ago" },
      { role: "tool", title: "read .github/workflows/test.yml", text: "- uses: actions/cache@v4\n  with:\n    path: vendor/bundle\n    key: bundle-${{ github.sha }}", time: "2 months ago" },
      { role: "tool", title: "read Gemfile.lock", text: "BUNDLED WITH\n   2.5.6", time: "2 months ago" },
      { role: "tool", title: "edit .github/workflows/test.yml", text: "- key: bundle-${{ github.sha }}\n+ key: bundle-${{ runner.os }}-ruby-${{ hashFiles('Gemfile.lock') }}\n+ restore-keys: |\n+   bundle-${{ runner.os }}-ruby-", time: "2 months ago" },
      { role: "tool", title: "bash git diff --check", text: "No whitespace errors detected", time: "2 months ago" },
      { role: "assistant", text: "Updated the cache key to include the OS and `Gemfile.lock`, with a restore prefix for older compatible bundles. This avoids reinstalling dependencies on source-only changes.", time: "2 months ago" }
    ] }
  ];

  const trustedGuideLinks = new Set([
    "https://github.com/melounvitek/gripi",
    "https://github.com/melounvitek/gripi/blob/master/docs/configuration.md",
    "https://github.com/melounvitek/gripi/blob/master/docs/examples.md",
    "https://pi.dev/",
    "https://tailscale.com/"
  ]);

  function safeGuideLink(link) {
    if (!link || !trustedGuideLinks.has(link.href)) return null;
    return { href: link.href, label: String(link.label || link.href) };
  }

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
      age: String(session.age || "just now"),
      pinned: !!session.pinned,
      messages: Array.isArray(session.messages) ? session.messages.filter((message) => allowedRoles.has(message?.role)).map((message) => ({
        role: message.role,
        text: String(message.text || ""),
        title: String(message.title || ""),
        time: String(message.time || ""),
        code: String(message.code || ""),
        link: safeGuideLink(message.link)
      })) : []
    };
  }

  function responseScript(prompt) {
    const safePrompt = String(prompt || "your question").trim() || "your question";
    const answer = `This is a prerecorded response to “${safePrompt}”. In a real Gripi session, the Pi coding-agent harness would now continue with full access to the selected project and its tools.\n\nThe static demo still mirrors the experience: messages appear live, tool activity is visible, and you can stop a response while it is streaming.`;
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

  function inlineCodeParts(text) {
    const parts = [];
    String(text || "").split(/(`[^`\n]+`)/).forEach((part) => {
      if (!part) return;
      if (part.startsWith("`") && part.endsWith("`")) parts.push({ type: "code", text: part.slice(1, -1) });
      else parts.push({ type: "text", text: part });
    });
    return parts;
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
  const sessionCatalog = initialSessions.map(({ id, name, project, age, pinned }) => ({ id, name, project, age, pinned }));
  global.GripiDemo = { playScript, responseScript, safeIdentityColor, safeGuideLink, inlineCodeParts, toolSummaryParts, formatDemoTimestamp: timeLabel, defaultSessionId, sessionCatalog, demoSessionCount: initialSessions.length, hasUnreadSessions: false };
  if (typeof document === "undefined") return;

  const storageKey = "gripi:static-demo:v13";
  const introSeenKey = "gripi:static-demo:intro-seen";
  let sessions = initialSessions;
  let currentId = defaultSessionId;
  const demoStartedAt = timeLabel();
  let streamController = null;
  let streamingEntry = null;
  let activeToolEntry = null;
  let switching = false;
  let switchGeneration = 0;
  let previousModalFocus = null;
  let findMatches = [];
  let findIndex = -1;
  let lastScrollTop = 0;
  let programmaticScroll = false;
  let programmaticScrollTimer = null;
  let autoScrollEnabled = true;
  let focusedView = false;
  let activityRunning = false;
  const focusedActivityMessageIds = new WeakMap();
  let focusedActivityMessageSequence = 0;
  let focusedActivitySignature = null;

  try {
    const stored = JSON.parse(localStorage.getItem(storageKey));
    if (Array.isArray(stored?.sessions) && stored.sessions.length) {
      const normalized = stored.sessions.map(normalizeSession).filter(Boolean);
      if (normalized.length) sessions = normalized;
    }
    if (sessions.some((session) => session.id === stored?.currentId)) currentId = stored.currentId;
  } catch (_error) {}

  const element = {
    history: document.getElementById("history-output"), live: document.getElementById("live-output"), scroll: document.getElementById("conversation-scroll"), sidebarScroll: document.querySelector(".session-sidebar-content"),
    pinned: document.getElementById("pinned-sessions-list"), sessions: document.getElementById("sessions-list"), empty: document.getElementById("sidebar-empty"),
    project: document.getElementById("project-filter"), projectTrigger: document.getElementById("project-select-trigger"), projectList: document.getElementById("project-select-listbox"),
    searchForm: document.getElementById("sidebar-session-search"), search: document.querySelector('#sidebar-session-search input[type="search"]'), clearFilters: document.querySelector("[data-sidebar-filters-clear]"),
    headerName: document.querySelector(".session-header-name"), headerProject: document.querySelector(".session-header-project"), form: document.getElementById("prompt-form"), prompt: document.querySelector(".prompt-form textarea"),
    panel: document.querySelector(".conversation-panel"), focusToggle: document.querySelector("[data-conversation-focus-toggle]"),
    state: document.querySelector(".composer-state"), stop: document.getElementById("stop-button"), commands: document.getElementById("command-list"), attachmentTray: document.querySelector(".attachment-tray"),
    treeTarget: document.querySelector("[data-demo-tree-target]"), treeTargetTitle: document.querySelector("[data-demo-tree-target-title]"), treeCurrentTitle: document.querySelector("[data-demo-tree-current-title]")
  };

  function currentSession() { return sessions.find((session) => session.id === currentId) || sessions[0]; }
  function introSeen() {
    try { return localStorage.getItem(introSeenKey) === "true"; } catch (_error) { return false; }
  }
  function markIntroSeen() {
    try { localStorage.setItem(introSeenKey, "true"); } catch (_error) {}
  }
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
  function setFocusedView(enabled) {
    focusedView = enabled;
    element.panel.classList.toggle("is-conversation-focused", focusedView);
    element.focusToggle.classList.toggle("is-active", focusedView);
    const action = focusedView ? "Show" : "Hide";
    const label = `${action} details`;
    const description = `${action} reasoning, tool calls, status updates, and errors`;
    element.focusToggle.setAttribute("title", description);
    element.focusToggle.setAttribute("aria-label", description);
    element.focusToggle.querySelector("[data-details-toggle-label]").textContent = label;
    element.focusToggle.querySelector("[data-hide-details-icon]").hidden = focusedView;
    element.focusToggle.querySelector("[data-show-details-icon]").hidden = !focusedView;
  }
  function focusedConversationMessage(message) {
    const error = message.classList.contains("message--error") || message.classList.contains("message--tool-error");
    return !error && (message.dataset.role === "user" || message.dataset.finalAssistantResponse === "true");
  }
  function focusedActivitySummary(messages) {
    const reasoningCount = messages.filter((message) => message.classList.contains("message--thinking")).length;
    const toolCount = messages.filter((message) => ["message--tool", "message--tool-call", "message--tool-transcript"].some((name) => message.classList.contains(name))).length;
    const errorCount = messages.filter((message) => message.classList.contains("message--error") || message.classList.contains("message--tool-error")).length;
    const otherCount = messages.filter((message) => !message.classList.contains("message--thinking") && !["message--tool", "message--tool-call", "message--tool-transcript", "message--error"].some((name) => message.classList.contains(name)) && !message.classList.contains("message--tool-error")).length;
    const parts = [];
    if (reasoningCount > 0) parts.push(`${reasoningCount} reasoning ${reasoningCount === 1 ? "step" : "steps"}`);
    if (toolCount > 0) parts.push(`${toolCount} tool ${toolCount === 1 ? "update" : "updates"}`);
    if (otherCount > 0) parts.push(`${otherCount} other ${otherCount === 1 ? "update" : "updates"}`);
    if (errorCount > 0) parts.push(`${errorCount} ${errorCount === 1 ? "error" : "errors"}`);
    return { text: parts.join(" · "), errorCount };
  }
  function refreshFocusedActivity() {
    const messages = [...element.scroll.querySelectorAll(".message")];
    const signature = `${activityRunning}|${messages.map((message) => {
      if (!focusedActivityMessageIds.has(message)) focusedActivityMessageIds.set(message, ++focusedActivityMessageSequence);
      return [focusedActivityMessageIds.get(message), focusedConversationMessage(message), message.classList.contains("message--thinking"), ["message--tool", "message--tool-call", "message--tool-transcript"].some((name) => message.classList.contains(name)), message.classList.contains("message--error") || message.classList.contains("message--tool-error")].join(":");
    }).join("|")}`;
    if (signature === focusedActivitySignature) return;
    focusedActivitySignature = signature;
    const summaries = [...element.scroll.querySelectorAll("[data-focus-activity-summary]")];
    const activeSummary = document.activeElement?.closest?.("[data-focus-activity-summary]");
    const activeSummaryIndex = summaries.indexOf(activeSummary);
    const activeGroupId = activeSummary?.dataset.focusActivitySummary;
    const focusAnchor = activeGroupId && messages.find((message) => message.dataset.focusActivityGroup === activeGroupId);
    const expandedMessages = new Set(element.scroll.querySelectorAll(".message.is-focus-activity-expanded"));
    summaries.forEach((summary) => summary.remove());
    const replacementSummaries = [];
    let replacementFocus = null;
    messages.forEach((message) => { message.classList.remove("is-focus-activity-expanded"); delete message.dataset.focusActivityGroup; });
    let groups = []; let group = [];
    messages.forEach((message) => {
      if (focusedConversationMessage(message)) { if (group.length) groups.push(group); group = []; }
      else group.push(message);
    });
    if (group.length) groups.push(group);
    const latestMessage = messages.at(-1);
    groups.forEach((messages, index) => {
      const groupId = `demo-${index}`;
      const expanded = messages.some((message) => expandedMessages.has(message));
      messages.forEach((message) => { message.dataset.focusActivityGroup = groupId; message.classList.toggle("is-focus-activity-expanded", expanded); });
      const summaryData = focusedActivitySummary(messages);
      const running = activityRunning && index === groups.length - 1 && messages.at(-1) === latestMessage;
      const summary = document.createElement("button");
      summary.type = "button";
      summary.className = `focus-activity-summary${summaryData.errorCount ? " has-errors" : ""}${expanded ? " is-expanded" : ""}${running ? " is-running" : ""}`;
      summary.dataset.focusActivitySummary = groupId;
      summary.setAttribute("aria-expanded", String(expanded));
      if (running) { const spinner = document.createElement("span"); spinner.className = "focus-activity-spinner"; spinner.setAttribute("aria-hidden", "true"); summary.append(spinner); }
      const text = document.createElement("span"); text.className = "focus-activity-summary-text"; text.textContent = summaryData.text; summary.append(text);
      if (summaryData.errorCount) { const error = document.createElement("span"); error.className = "focus-activity-error-count"; error.textContent = `${summaryData.errorCount} ${summaryData.errorCount === 1 ? "error" : "errors"}`; error.setAttribute("aria-hidden", "true"); summary.append(error); }
      messages[0].before(summary);
      replacementSummaries.push(summary);
      if (focusAnchor && messages.includes(focusAnchor)) replacementFocus = summary;
    });
    if (activeSummary && !replacementFocus) replacementFocus = replacementSummaries[Math.min(Math.max(activeSummaryIndex, 0), replacementSummaries.length - 1)] || element.focusToggle;
    replacementFocus?.focus({ preventScroll: true });
  }
  function timeLabel(date = new Date()) { const pad = (value) => String(value).padStart(2, "0"); return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`; }
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

  function appendInlineCode(target, text) {
    inlineCodeParts(text).forEach((part) => {
      if (part.type === "code") {
        const code = document.createElement("code");
        code.textContent = part.text;
        target.append(code);
      } else {
        target.append(document.createTextNode(part.text));
      }
    });
  }

  function toolSummaryParts(title) {
    const text = String(title || "Tool activity").trim();
    const match = text.match(/^(bash|read|edit|write)\b(?:\s+(.+))?$/);
    if (!match) return [{ type: "text", text }];
    const command = match[1];
    const argument = (match[2] || "").trim();
    if (command === "bash") return [{ type: "text", text: argument ? `$ ${argument}` : "bash" }];
    return [{ type: "command", text: command }, ...(argument ? [{ type: "path", text: argument }] : [])];
  }

  function appendToolSummary(target, title) {
    toolSummaryParts(title).forEach((part, index) => {
      if (index > 0) target.append(document.createTextNode(" "));
      if (part.type === "text") {
        target.append(document.createTextNode(part.text));
        return;
      }
      const span = document.createElement("span");
      span.className = part.type === "command" ? "tool-command" : "tool-path";
      span.textContent = part.text;
      target.append(span);
    });
  }

  function messageArticle(message, live) {
    const article = document.createElement("article");
    const role = message.role;
    const roleKey = role === "thinking" || role === "tool" ? "assistant" : role === "compaction" ? "status" : role;
    article.className = `message message--${roleKey}${role === "thinking" ? " message--thinking" : ""}${role === "tool" ? " message--compact message--tool-call" : ""}${role === "compaction" ? " message--compact" : ""}${live ? " message--live" : ""}`;
    article.dataset.role = roleKey;
    if (role === "assistant" && !live && message.completed !== false) article.dataset.finalAssistantResponse = "true";
    const header = document.createElement("header");
    header.className = "message-header";
    const label = document.createElement("div");
    label.className = "role";
    label.textContent = role === "assistant" || role === "thinking" ? "pi" : role === "compaction" ? "status" : role;
    const meta = document.createElement("div");
    meta.className = "message-meta";
    meta.textContent = message.time || demoStartedAt;
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
      appendToolSummary(compact, message.title || "Tool activity");
      summary.append(compact);
      details.append(summary);
      article.append(details);
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
        String(message.text || "").split(/\n\n+/).forEach((paragraph) => { const p = document.createElement("p"); appendInlineCode(p, paragraph); body.append(p); });
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

  function touchSessionActivity(session) {
    session.age = "just now";
    if (session.pinned) return;
    sessions = [session, ...sessions.filter((item) => item !== session)];
  }

  function renderSidebar() {
    const scrollTop = element.sidebarScroll?.scrollTop || 0;
    element.pinned.replaceChildren(); element.sessions.replaceChildren();
    const visible = filteredSessions();
    visible.filter((session) => session.pinned).forEach((session) => element.pinned.append(sessionRow(session)));
    visible.filter((session) => !session.pinned).forEach((session) => element.sessions.append(sessionRow(session)));
    element.empty.hidden = visible.length > 0;
    element.clearFilters.hidden = !element.project.value && !element.search.value;
    if (element.sidebarScroll) element.sidebarScroll.scrollTop = scrollTop;
  }

  function renderConversation() {
    element.history.replaceChildren(); element.live.replaceChildren();
    currentSession().messages.forEach((message) => element.history.append(messageArticle(message, false)));
    refreshFocusedActivity();
    requestAnimationFrame(() => {
      element.scroll.scrollTop = element.scroll.scrollHeight;
      lastScrollTop = element.scroll.scrollTop;
      autoScrollEnabled = true;
    });
  }

  function renderHeader() {
    const session = currentSession();
    element.headerName.textContent = session.name;
    applyIdentity(element.headerProject, session);
    element.headerProject.querySelector(".session-header-project-icon").textContent = session.monogram;
    element.headerProject.querySelector(".session-header-project-label").textContent = session.project;
    const relatedSession = sessions.find(({ id }) => id === (session.id === defaultSessionId ? "new-to-pi" : defaultSessionId));
    element.treeTarget.dataset.demoTreeTarget = relatedSession.id;
    element.treeTargetTitle.textContent = relatedSession.name;
    element.treeCurrentTitle.textContent = session.name;
    document.title = `${session.name} · Gripi demo`;
  }

  function setRunning(running, text) {
    activityRunning = running;
    focusedActivitySignature = null;
    refreshFocusedActivity();
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
    element.scroll.scrollTo(options);
    finishProgrammaticScrollSoon();
  }
  function programmaticScrollIntoView(target) {
    programmaticScroll = true;
    target.scrollIntoView({ block: "center" });
    finishProgrammaticScrollSoon();
  }
  function scrollLatest(force = false) {
    if (!autoScrollEnabled && !force) return;
    if (force) autoScrollEnabled = true;
    programmaticScrollTo({ top: element.scroll.scrollHeight, behavior: "smooth" });
  }

  function cancelStream() {
    if (!streamController) return;
    streamController.abort(); streamController = null;
    streamingEntry?.article.classList.remove("message--streaming");
    if (streamingEntry && !streamingEntry.message.text) {
      streamingEntry.session.messages = streamingEntry.session.messages.filter((message) => message !== streamingEntry.message);
      streamingEntry.article.remove();
    }
    if (activeToolEntry) {
      activeToolEntry.article.classList.remove("message--live");
      if (activeToolEntry.message.text === "Running…") activeToolEntry.message.text = "Stopped before the simulated tool completed.";
    }
    streamingEntry = null; activeToolEntry = null;
    setRunning(false);
    persist();
    refreshFocusedActivity();
  }

  function appendStreamEvent(event, session) {
    if (event.type === "status") setRunning(true, event.text);
    if (event.type === "thinking") {
      const message = { role: "thinking", text: event.text, time: timeLabel() }; session.messages.push(message); element.live.append(messageArticle(message, true));
    }
    if (event.type === "tool_start") {
      const message = { role: "tool", title: event.title, text: "Running…", time: timeLabel() }; session.messages.push(message);
      const article = messageArticle(message, true); article.classList.add("message--live"); element.live.append(article); activeToolEntry = { article, message }; setRunning(true, "Using tools…");
    }
    if (event.type === "tool_end" && activeToolEntry) { activeToolEntry.message.text = event.text; activeToolEntry.article.classList.remove("message--live"); setRunning(true, "Pi is responding…"); }
    if (event.type === "assistant_start") {
      const message = { role: "assistant", text: "", time: timeLabel(), completed: false }; session.messages.push(message);
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
      if (streamingEntry) {
        streamingEntry.message.completed = true;
        streamingEntry.article.dataset.finalAssistantResponse = "true";
      }
      streamingEntry?.article.classList.remove("message--streaming"); streamController = null; streamingEntry = null; activeToolEntry = null; setRunning(false); persist();
    }
    if (["thinking", "tool_start", "assistant_start", "done"].includes(event.type)) refreshFocusedActivity();
    refreshOpenFind();
    scrollLatest();
  }

  async function submitPrompt() {
    const prompt = element.prompt.value.trim();
    if (!prompt || streamController || switching) return;
    if (prompt.startsWith("/")) { handleSlash(prompt.split(/\s/)[0].slice(1)); element.prompt.value = ""; persistDraft(); element.commands.classList.remove("is-visible"); return; }
    const streamSession = currentSession();
    const message = { role: "user", text: prompt, time: timeLabel() };
    streamSession.messages.push(message); touchSessionActivity(streamSession); renderSidebar(); element.live.append(messageArticle(message, true)); element.prompt.value = ""; persistDraft(); refreshFocusedActivity(); refreshOpenFind();
    element.attachmentTray.replaceChildren(); element.attachmentTray.classList.remove("has-attachments");
    setRunning(true, "Sending…"); persist(); scrollLatest(true);
    streamController = new AbortController();
    await playScript(responseScript(prompt), { signal: streamController.signal, onEvent: (event) => appendStreamEvent(event, streamSession) });
  }

  function modalIsOpen() { return !!document.querySelector("[data-modal]:not([hidden])"); }
  function openModal(name, returnFocus = document.activeElement) {
    const modal = document.querySelector(`[data-modal="${name}"]`); if (!modal) return;
    previousModalFocus = returnFocus; modal.hidden = false; document.querySelector(".app-shell").inert = true;
    (modal.querySelector("[data-modal-default-focus]") || modal.querySelector("button, input, select"))?.focus();
  }
  function closeModal(modal) { if (!modal) return; modal.hidden = true; document.querySelector(".app-shell").inert = false; if (modal.dataset.modal === "demo-intro-modal") markIntroSeen(); previousModalFocus?.focus(); previousModalFocus = null; }
  function handleSlash(command) {
    const modals = { new: "new-session-modal", fork: "fork-session-modal", tree: "tree-session-modal", model: "model-settings-modal" };
    if (modals[command]) openModal(modals[command]);
    else if (command === "compact") { const session = currentSession(); const message = { role: "compaction", title: "Conversation compacted", text: "Static demo compaction summary. In production this is generated by Pi.", time: timeLabel() }; session.messages.push(message); touchSessionActivity(session); renderSidebar(); element.live.append(messageArticle(message, true)); persist(); refreshFocusedActivity(); refreshOpenFind(); scrollLatest(true); }
  }

  function switchSession(id) {
    if (!sessions.some((session) => session.id === id)) return;
    const generation = ++switchGeneration;
    switching = true;
    element.prompt.disabled = true;
    persistDraft(); cancelStream(); resetFind(true); document.body.classList.add("session-switching");
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

  function refreshOpenFind() {
    if (!document.querySelector("[data-current-session-find]").hidden) updateFind();
  }

  function updateFind() {
    clearFindMatches();
    const query = document.querySelector("[data-current-session-find-input]").value.trim();
    const conversationOnly = document.querySelector("[data-current-session-find-conversation-only]").checked;
    findMatches = [];
    if (query) {
      const selector = conversationOnly ? ".message-body" : ".message";
      element.scroll.querySelectorAll(selector).forEach((root) => {
        const message = root.closest(".message");
        if (focusedView && !focusedConversationMessage(message) && !message?.classList.contains("is-focus-activity-expanded")) return;
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
    const activity = event.target.closest("[data-focus-activity-summary]"); if (activity) {
      const groupId = activity.dataset.focusActivitySummary;
      const expanded = activity.getAttribute("aria-expanded") !== "true";
      activity.setAttribute("aria-expanded", String(expanded)); activity.classList.toggle("is-expanded", expanded);
      element.scroll.querySelectorAll("[data-focus-activity-group]").forEach((message) => { if (message.dataset.focusActivityGroup === groupId) message.classList.toggle("is-focus-activity-expanded", expanded); });
      refreshOpenFind(); return;
    }
    const copy = event.target.closest("[data-copy-target]"); if (copy) {
      const text = copy.closest(".message").querySelector(".message-body")?.textContent || "";
      const fallback = () => { const textarea = document.createElement("textarea"); textarea.value = text; textarea.className = "visually-hidden"; document.body.append(textarea); textarea.select(); const copied = document.execCommand("copy"); textarea.remove(); return copied; };
      Promise.resolve(navigator.clipboard?.writeText ? navigator.clipboard.writeText(text).then(() => true).catch(fallback) : fallback()).then((copied) => { copy.textContent = copied ? "Copied" : "Copy failed"; setTimeout(() => { copy.textContent = "Copy"; }, 1200); }); return;
    }
    if (!event.target.closest("[data-project-select]") && !element.projectList.hidden) { element.projectList.hidden = true; element.projectTrigger.setAttribute("aria-expanded", "false"); }
    if (event.target.closest("[data-demo-disabled]")) event.preventDefault();
  });

  element.scroll.addEventListener("scroll", () => {
    const current = element.scroll.scrollTop;
    if (programmaticScroll) { lastScrollTop = current; finishProgrammaticScrollSoon(); return; }
    const maximum = Math.max(0, element.scroll.scrollHeight - element.scroll.clientHeight);
    autoScrollEnabled = maximum - current < 120;
    lastScrollTop = current;
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
  document.querySelector("[data-notification-toggle]").addEventListener("click", (event) => { const enabled = !event.currentTarget.classList.contains("is-enabled"); event.currentTarget.classList.toggle("is-enabled", enabled); event.currentTarget.classList.toggle("is-disabled", !enabled); event.currentTarget.querySelector("[data-notification-toggle-state]").textContent = enabled ? "Demo on" : "Demo off"; });
  element.focusToggle.addEventListener("click", () => {
    setFocusedView(!focusedView);
    refreshOpenFind();
  });
  element.form.addEventListener("submit", (event) => { event.preventDefault(); submitPrompt(); });
  element.prompt.addEventListener("input", () => { const slash = element.prompt.value.startsWith("/"); element.commands.classList.toggle("is-visible", slash); if (slash) element.commands.open = true; persistDraft(); });
  element.prompt.addEventListener("keydown", (event) => { if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); submitPrompt(); } });
  element.stop.addEventListener("click", cancelStream);

  document.querySelector("[data-current-session-find-input]").addEventListener("input", updateFind);
  document.querySelector("[data-current-session-find-conversation-only]").addEventListener("change", updateFind);
  document.querySelector("[data-current-session-find-previous]").addEventListener("click", () => moveFind(-1));
  document.querySelector("[data-current-session-find-next]").addEventListener("click", () => moveFind(1));
  document.querySelector("[data-current-session-find-close]").addEventListener("click", () => resetFind(true));
  document.addEventListener("keydown", (event) => {
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "f" && !modalIsOpen()) { event.preventDefault(); const find = document.querySelector("[data-current-session-find]"); find.hidden = false; find.querySelector("input").focus(); }
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
    sessions.unshift({ id, name: "New local demo session", project, ...identity, age: "just now", pinned: false, messages: [{ role: "assistant", text: "This representative session was created locally. Enter a prompt to try streaming.", time: timeLabel() }] });
    closeModal(event.target.closest("[data-modal]")); switchSession(id);
  });
  document.querySelectorAll("[data-demo-fork]").forEach((button) => button.addEventListener("click", () => { const source = currentSession(); const id = `fork-${Date.now()}`; sessions.push({ ...source, id, name: `${source.name} (fork)`, age: "just now", pinned: false, messages: source.messages.slice(0, 4).map((message) => ({ ...message })) }); closeModal(button.closest("[data-modal]")); switchSession(id); }));
  document.querySelectorAll("[data-demo-tree]").forEach((button) => button.addEventListener("click", () => { closeModal(button.closest("[data-modal]")); if (button.dataset.demoTreeTarget) switchSession(button.dataset.demoTreeTarget); }));
  document.querySelector(".model-settings-form").addEventListener("submit", (event) => { event.preventDefault(); const model = new FormData(event.target).get("model"); const thinking = new FormData(event.target).get("thinking"); document.querySelector('[data-status-key="model"] .session-status-value').textContent = `${model} (${thinking})`; closeModal(event.target.closest("[data-modal]")); });
  document.querySelector("[data-model-search]").addEventListener("input", (event) => { const query = event.target.value.toLowerCase(); document.querySelectorAll(".model-option").forEach((option) => { option.hidden = !option.textContent.toLowerCase().includes(query); }); });

  renderHeader(); renderConversation(); renderSidebar(); loadDraft();
  if (!introSeen()) openModal("demo-intro-modal", null);
})(globalThis);
