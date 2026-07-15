require "minitest/autorun"
require "json"
require "open3"

class FrontendControllersJsTest < Minitest::Test
  ASSETS = File.expand_path("../public/assets", __dir__)

  def test_gateway_update_controller_applies_states_and_navigates_to_a_new_instance
    results = run_javascript(<<~JS)
      const { GatewayUpdateController } = await import(#{module_url("gateway_update_controller.js").to_json});

      const control = element();
      const button = element();
      const message = element();
      control.querySelector = (selector) => selector.includes("button") ? button : message;
      const document = eventTarget({
        body: { dataset: { gatewayInstanceId: "old-instance" } },
        querySelector: (selector) => selector === "[data-gateway-update]" ? control : null
      });
      const navigations = [];
      const window = eventTarget({
        location: {
          href: "https://example.test/?session=one&_gateway_updated=previous",
          replace: (url) => navigations.push(url)
        },
        history: { state: null, replaceState() {} },
        confirm: () => true
      });
      globalThis.fetch = async () => response({
        state: "idle",
        instanceId: "new-instance",
        currentSha: "abc123"
      });

      const controller = new GatewayUpdateController(document, window, null);
      controller.apply({ state: "available", targetSha: "def456", summary: "Update summary", message: "Update ready" });
      const available = snapshot(control, button, message);
      controller.apply({ state: "dependency_failed", message: "Install failed" });
      const failed = snapshot(control, button, message);
      controller.apply({ state: "rollback_failed", message: "Rollback failed" });
      const rollbackFailed = snapshot(control, button, message);
      await controller.check();

      console.log(JSON.stringify({ available, failed, rollbackFailed, navigations }));

      function eventTarget(properties = {}) {
        return Object.assign({ addEventListener() {} }, properties);
      }
      function element() {
        const classes = new Set();
        return {
          hidden: false,
          textContent: "",
          title: "",
          classList: {
            toggle(name, enabled) { enabled ? classes.add(name) : classes.delete(name); },
            contains(name) { return classes.has(name); }
          }
        };
      }
      function snapshot(control, button, message) {
        return {
          hidden: control.hidden,
          error: control.classList.contains("is-error"),
          buttonHidden: button.hidden,
          buttonText: button.textContent,
          title: button.title,
          message: message.textContent
        };
      }
      function response(payload) {
        return { ok: true, json: async () => payload };
      }
    JS

    assert_equal({
      "hidden" => false,
      "error" => false,
      "buttonHidden" => false,
      "buttonText" => "Update to def456",
      "title" => "Update summary",
      "message" => "Update ready"
    }, results.fetch("available"))
    assert_equal true, results.dig("failed", "error")
    assert_equal "Retry update", results.dig("failed", "buttonText")
    assert_equal "Install failed", results.dig("failed", "message")
    assert_equal true, results.dig("rollbackFailed", "buttonHidden")
    assert_equal ["https://example.test/?session=one&_gateway_updated=abc123"], results.fetch("navigations")
  end

  def test_gateway_update_controller_broadcasts_and_polls_update_progress
    results = run_javascript(<<~JS)
      const { GatewayUpdateController } = await import(#{module_url("gateway_update_controller.js").to_json});

      const control = element();
      const button = element();
      const message = element();
      control.querySelector = (selector) => selector.includes("button") ? button : message;
      const document = eventTarget({
        body: { dataset: { gatewayInstanceId: "instance" } },
        querySelector: () => control
      });
      const window = eventTarget({
        location: { href: "https://example.test/", replace() {} },
        history: { state: null, replaceState() {} },
        confirm: () => true
      });
      const requests = [];
      globalThis.fetch = async (url, options = {}) => {
        requests.push([url, options.method || "GET"]);
        return response({ state: "restarting", targetSha: "next" });
      };
      const timers = [];
      globalThis.setTimeout = (callback, delay) => { timers.push({ callback, delay }); return timers.length; };
      globalThis.clearTimeout = () => {};
      globalThis.setInterval = () => 1;
      class FakeBroadcastChannel {
        constructor(name) { this.name = name; this.messages = []; FakeBroadcastChannel.instance = this; }
        addEventListener(_type, listener) { this.listener = listener; }
        postMessage(message) { this.messages.push(message); }
      }
      globalThis.BroadcastChannel = FakeBroadcastChannel;

      const controller = new GatewayUpdateController(document, window, FakeBroadcastChannel);
      controller.apply({ state: "available", targetSha: "next" });
      await controller.start();
      FakeBroadcastChannel.instance.listener({ data: { type: "updating" } });

      console.log(JSON.stringify({
        requests,
        messages: FakeBroadcastChannel.instance.messages,
        stateMessage: message.textContent,
        timerDelays: timers.map((timer) => timer.delay)
      }));

      function eventTarget(properties = {}) { return Object.assign({ addEventListener() {} }, properties); }
      function element() {
        return { hidden: false, textContent: "", title: "", classList: { toggle() {} } };
      }
      function response(payload) { return { ok: true, json: async () => payload }; }
    JS

    assert_equal [["/gateway-update", "POST"]], results.fetch("requests")
    assert_equal [{ "type" => "updating" }], results.fetch("messages")
    assert_equal "Restarting gateway…", results.fetch("stateMessage")
    assert_equal [1000, 1000], results.fetch("timerDelays")
  end

  def test_browser_access_controller_owns_polling_display_resolution_and_pause
    results = run_javascript(<<~JS)
      const { BrowserAccessRequestController } = await import(#{module_url("access_request_controllers.js").to_json});
      const elements = accessElements("browser");
      const listeners = {};
      const document = {
        body: { dataset: { browserAccessEnabled: "true" } },
        addEventListener(type, listener) { listeners[type] = listener; },
        querySelector(selector) {
          if (selector === "[data-modal]:not([hidden])") return null;
          return elements[selector] || null;
        }
      };
      const requests = [];
      let pendingRequests = 0;
      globalThis.fetch = async (url, options = {}) => {
        requests.push([url, options.method || "GET", options.body?.get?.("code") || null]);
        if (url.endsWith("pending") && pendingRequests++ === 0) return response({ requests: [{ code: "B7", ip: "127.0.0.1", user_agent: "Browser" }] });
        return response({ requests: [] });
      };
      const timers = new Map();
      let nextTimer = 0;
      globalThis.setTimeout = (callback, delay) => { const id = ++nextTimer; timers.set(id, { callback, delay }); return id; };
      globalThis.clearTimeout = (id) => timers.delete(id);
      globalThis.FormData = class {
        constructor() { this.values = new Map(); }
        set(key, value) { this.values.set(key, value); }
        get(key) { return this.values.get(key); }
      };

      const controller = new BrowserAccessRequestController(document);
      await controller.resume();
      const shown = {
        visible: elements.overlay.classList.contains("is-visible"),
        title: elements.title.textContent,
        meta: elements.meta.textContent,
        timers: timers.size
      };
      await listeners.click({ target: { closest: (selector) => selector === "[data-browser-access-allow]" } });
      controller.pause();

      console.log(JSON.stringify({ shown, requests, visibleAfter: elements.overlay.classList.contains("is-visible"), timers: timers.size }));

      function accessElements(kind) {
        const classes = new Set();
        const result = {
          overlay: { classList: { add(name) { classes.add(name); }, remove(name) { classes.delete(name); }, contains(name) { return classes.has(name); } } },
          title: { textContent: "" }, meta: { textContent: "" }, allow: {}, deny: {}
        };
        return Object.assign(result, Object.fromEntries(Object.entries(result).map(([name, value]) => [`[data-${kind}-access-${name}]`, value])));
      }
      function response(payload) { return { ok: true, json: async () => payload }; }
    JS

    assert_equal true, results.dig("shown", "visible")
    assert_equal "New browser requests access: B7", results.dig("shown", "title")
    assert_equal "127.0.0.1 · Browser", results.dig("shown", "meta")
    assert_equal 1, results.dig("shown", "timers")
    assert_equal [
      ["/browser-access/pending", "GET", nil],
      ["/browser-access/approve", "POST", "B7"],
      ["/browser-access/pending", "GET", nil]
    ], results.fetch("requests")
    assert_equal false, results.fetch("visibleAfter")
    assert_equal 0, results.fetch("timers")
  end

  def test_access_controller_pause_aborts_and_ignores_an_in_flight_poll
    results = run_javascript(<<~JS)
      const { BrowserAccessRequestController } = await import(#{module_url("access_request_controllers.js").to_json});
      const classes = new Set();
      const elements = {
        "[data-browser-access-overlay]": { classList: { add: (name) => classes.add(name), remove: (name) => classes.delete(name) } },
        "[data-browser-access-title]": { textContent: "" },
        "[data-browser-access-meta]": { textContent: "" }
      };
      const document = {
        body: { dataset: { browserAccessEnabled: "true" } },
        addEventListener() {},
        querySelector: (selector) => selector === "[data-modal]:not([hidden])" ? null : elements[selector]
      };
      let finishRequest;
      let requestSignal;
      globalThis.fetch = (_url, options) => {
        requestSignal = options.signal || { aborted: false };
        return new Promise((resolve) => { finishRequest = resolve; });
      };
      const timers = [];
      globalThis.setTimeout = (callback) => { timers.push(callback); return timers.length; };
      globalThis.clearTimeout = () => {};

      const controller = new BrowserAccessRequestController(document);
      const poll = controller.resume();
      controller.pause();
      finishRequest({ ok: true, json: async () => ({ requests: [{ code: "STALE", ip: "127.0.0.1" }] }) });
      await poll;

      console.log(JSON.stringify({
        aborted: requestSignal.aborted,
        visible: classes.has("is-visible"),
        currentCode: controller.currentCode,
        timers: timers.length
      }));
    JS

    assert_equal true, results.fetch("aborted")
    assert_equal false, results.fetch("visible")
    assert_nil results.fetch("currentCode")
    assert_equal 0, results.fetch("timers")
  end

  def test_access_resolution_invalidates_an_overlapping_poll
    results = run_javascript(<<~JS)
      const { BrowserAccessRequestController } = await import(#{module_url("access_request_controllers.js").to_json});
      const classes = new Set();
      const elements = {
        "[data-browser-access-overlay]": { classList: { add: (name) => classes.add(name), remove: (name) => classes.delete(name) } },
        "[data-browser-access-title]": { textContent: "" },
        "[data-browser-access-meta]": { textContent: "" }
      };
      const document = {
        body: { dataset: { browserAccessEnabled: "true" } }, addEventListener() {},
        querySelector: (selector) => selector === "[data-modal]:not([hidden])" ? null : elements[selector]
      };
      let pendingCount = 0;
      let finishStalePoll;
      let staleSignal;
      let abortedBeforePost = false;
      let postCount = 0;
      globalThis.fetch = async (url, options = {}) => {
        if (!url.endsWith("pending")) {
          postCount += 1;
          abortedBeforePost = staleSignal.aborted;
          return response({});
        }
        pendingCount += 1;
        if (pendingCount === 1) return response({ requests: [{ code: "B7" }] });
        if (pendingCount === 2) {
          staleSignal = options.signal;
          return new Promise((resolve) => { finishStalePoll = () => resolve(response({ requests: [{ code: "B7" }] })); });
        }
        return response({ requests: [] });
      };
      const timers = new Map();
      let timerId = 0;
      globalThis.setTimeout = (callback) => { const id = ++timerId; timers.set(id, callback); return id; };
      globalThis.clearTimeout = (id) => timers.delete(id);
      globalThis.FormData = class { set() {} };

      const controller = new BrowserAccessRequestController(document);
      await controller.resume();
      const scheduledPoll = [...timers.values()][0];
      timers.clear();
      scheduledPoll();
      await Promise.all([controller.resolve("approve"), controller.resolve("approve")]);
      finishStalePoll();
      await Promise.resolve();
      await Promise.resolve();

      console.log(JSON.stringify({
        aborted: staleSignal.aborted,
        abortedBeforePost,
        visible: classes.has("is-visible"),
        currentCode: controller.currentCode,
        pendingCount,
        postCount,
        timers: timers.size
      }));
      function response(payload) { return { ok: true, json: async () => payload }; }
    JS

    assert_equal true, results.fetch("aborted")
    assert_equal true, results.fetch("abortedBeforePost")
    assert_equal false, results.fetch("visible")
    assert_nil results.fetch("currentCode")
    assert_equal 3, results.fetch("pendingCount")
    assert_equal 1, results.fetch("postCount")
    assert_equal 1, results.fetch("timers")
  end

  def test_workspace_access_controller_preserves_workspace_copy_and_endpoint
    results = run_javascript(<<~JS)
      const { WorkspaceAccessRequestController } = await import(#{module_url("access_request_controllers.js").to_json});
      const classes = new Set();
      const elements = {
        "[data-workspace-access-overlay]": { classList: { add: (name) => classes.add(name), remove: (name) => classes.delete(name) } },
        "[data-workspace-access-title]": { textContent: "" },
        "[data-workspace-access-meta]": { textContent: "" },
        "[data-workspace-access-allow]": {}, "[data-workspace-access-deny]": {}
      };
      const document = {
        body: { dataset: { workspaceAccessEnabled: "true" } },
        addEventListener() {},
        querySelector: (selector) => selector === "[data-modal]:not([hidden])" ? null : elements[selector]
      };
      const urls = [];
      globalThis.fetch = async (url) => { urls.push(url); return response({ requests: [{ code: "W9" }] }); };
      globalThis.setTimeout = () => 1;
      globalThis.clearTimeout = () => {};

      const controller = new WorkspaceAccessRequestController(document);
      await controller.resume();
      console.log(JSON.stringify({ urls, title: elements["[data-workspace-access-title]"].textContent, meta: elements["[data-workspace-access-meta]"].textContent }));
      function response(payload) { return { ok: true, json: async () => payload }; }
    JS

    assert_equal ["/workspace-access/pending"], results.fetch("urls")
    assert_equal "New workspace requests access: W9", results.fetch("title")
    assert_equal "Approve only if a trusted colleague is waiting for this code.", results.fetch("meta")
  end

  def test_project_select_controller_repeats_initialize_and_destroy_without_leaking_accessibility_state
    results = run_javascript(<<~JS)
      const { ProjectSelectController } = await import(#{module_url("project_select_controller.js").to_json});
      #{dom_fakes}

      const document = new FakeDocument();
      const window = new FakeEventTarget();
      window.Event = class { constructor(type, options) { this.type = type; this.bubbles = options?.bubbles; } };
      window.matchMedia = () => ({ matches: true });
      const root = new FakeElement("section");
      const wrapper = new FakeElement("div", ["[data-project-select]"]);
      const label = new FakeElement("label");
      label.id = "project-label";
      label.textContent = " Project folder ";
      const select = new FakeElement("select");
      select.id = "project";
      select.tabIndex = 3;
      select.setAttribute("aria-labelledby", label.id);
      select.setAttribute("aria-hidden", "false");
      label.htmlFor = select.id;
      select.options = [nativeOption("/one", "One"), nativeOption("/two", "Two")];
      select.selectedIndex = 1;
      Object.defineProperty(select, "selectedOptions", { get() { return [this.options[this.selectedIndex]]; } });
      wrapper.append(select);
      root.append(wrapper);
      document.body.append(root, label);
      document.ids.set(label.id, label);

      const controller = new ProjectSelectController(document, window);
      controller.initialize(root);
      controller.initialize(root);
      const first = wrapper._projectSelectState;
      controller.open(wrapper);
      const enhanced = {
        wrapperChildren: wrapper.children.length,
        bodyListboxes: document.body.children.filter((child) => child.classList.contains("project-select-listbox")).length,
        triggerLabel: first.trigger.getAttribute("aria-label"),
        triggerExpanded: first.trigger.getAttribute("aria-expanded"),
        activeDescendant: first.trigger.getAttribute("aria-activedescendant"),
        listboxLabelledBy: first.listbox.getAttribute("aria-labelledby"),
        nativeHidden: select.getAttribute("aria-hidden"),
        nativeTabIndex: select.tabIndex,
        labelFor: label.htmlFor,
        documentClicks: document.listenerCount("click"),
        windowResizes: window.listenerCount("resize")
      };

      controller.destroy(root);
      const destroyed = {
        stateRemoved: !wrapper._projectSelectState,
        wrapperChildren: wrapper.children.length,
        bodyListboxes: document.body.children.filter((child) => child.classList.contains("project-select-listbox")).length,
        nativeClassRemoved: !select.classList.contains("project-select-native-hidden"),
        nativeHidden: select.getAttribute("aria-hidden"),
        nativeTabIndex: select.tabIndex,
        labelFor: label.htmlFor
      };
      controller.initialize(root);
      controller.destroy(root);

      console.log(JSON.stringify({
        enhanced,
        destroyed,
        finalWrapperChildren: wrapper.children.length,
        finalBodyListboxes: document.body.children.filter((child) => child.classList.contains("project-select-listbox")).length,
        documentClicks: document.listenerCount("click"),
        windowResizes: window.listenerCount("resize")
      }));

      function nativeOption(value, text) {
        const option = new FakeElement("option");
        option.value = value;
        option.textContent = text;
        option.dataset.projectMonogram = text[0];
        option.dataset.projectBackground = "#000";
        option.dataset.projectForeground = "#fff";
        return option;
      }
    JS

    assert_equal 2, results.dig("enhanced", "wrapperChildren")
    assert_equal 1, results.dig("enhanced", "bodyListboxes")
    assert_equal "Project folder", results.dig("enhanced", "triggerLabel")
    assert_equal "true", results.dig("enhanced", "triggerExpanded")
    assert_match(/-option-1\z/, results.dig("enhanced", "activeDescendant"))
    assert_match(/-trigger\z/, results.dig("enhanced", "listboxLabelledBy"))
    assert_equal "true", results.dig("enhanced", "nativeHidden")
    assert_equal(-1, results.dig("enhanced", "nativeTabIndex"))
    assert_match(/-trigger\z/, results.dig("enhanced", "labelFor"))
    assert_equal true, results.dig("destroyed", "stateRemoved")
    assert_equal 1, results.dig("destroyed", "wrapperChildren")
    assert_equal 0, results.dig("destroyed", "bodyListboxes")
    assert_equal true, results.dig("destroyed", "nativeClassRemoved")
    assert_equal "false", results.dig("destroyed", "nativeHidden")
    assert_equal 3, results.dig("destroyed", "nativeTabIndex")
    assert_equal "project", results.dig("destroyed", "labelFor")
    assert_equal 1, results.fetch("finalWrapperChildren")
    assert_equal 0, results.fetch("finalBodyListboxes")
    assert_equal 1, results.fetch("documentClicks")
    assert_equal 1, results.fetch("windowResizes")
  end

  def test_new_session_form_controller_cancels_pending_work_and_ignores_stale_responses
    results = run_javascript(<<~JS)
      const { NewSessionFormController } = await import(#{module_url("new_session_form_controller.js").to_json});
      #{dom_fakes}

      const document = new FakeDocument();
      const window = new FakeEventTarget();
      window.location = { origin: "https://example.test" };
      const form = new FakeElement("form", [".new-session-cwd-form"]);
      form.dataset.cwdBrowserUrl = "/sessions/browse_cwd";
      const input = field("[data-new-session-cwd-input]");
      const hidden = field("[data-new-session-cwd-value]");
      const status = field("[data-new-session-cwd-message]");
      const submit = field("[data-new-session-submit]");
      const list = field("[data-new-session-cwd-suggestions]");
      const pathFields = field("[data-new-session-path-fields]");
      list.hidden = true;
      pathFields.hidden = false;
      form.append(input, hidden, status, submit, list, pathFields);
      document.body.append(form);

      const timers = new Map();
      let nextTimer = 0;
      globalThis.setTimeout = (callback) => { const id = ++nextTimer; timers.set(id, callback); return id; };
      globalThis.clearTimeout = (id) => timers.delete(id);
      const requests = [];
      globalThis.fetch = (url, options) => new Promise((resolve) => requests.push({ url: String(url), options, resolve }));

      const projectSelect = { sync() {} };
      const controller = new NewSessionFormController(document, window, projectSelect);
      controller.initialize(form);
      controller.initialize(form);
      const listenerCounts = Object.fromEntries(["input", "change", "click", "focusout", "keydown"].map((type) => [type, form.listenerCount(type)]));

      input.value = "/cancelled-before-fetch";
      controller.validate(form);
      controller.destroy(form);
      const cancelledTimerCount = timers.size;
      controller.initialize(form);

      input.value = "/old";
      controller.validate(form, { delay: 0 });
      runNextTimer();
      const oldRequest = requests.shift();
      input.value = "/new";
      controller.validate(form, { delay: 0 });
      runNextTimer();
      const newRequest = requests.shift();
      newRequest.resolve(response({ valid: true, cwd: "/new", directories: [] }));
      await flush();
      const afterNew = snapshot();
      oldRequest.resolve(response({ valid: false, error: "Old response won", directories: [] }));
      await flush();
      const afterOld = snapshot();

      hidden.value = "";
      input.value = "/reopened";
      controller.validate(form, { delay: 0 });
      runNextTimer();
      const closedRequest = requests.shift();
      controller.close(form);
      controller.open(form);
      runNextTimer();
      const reopenedRequest = requests.shift();
      reopenedRequest.resolve(response({ valid: true, cwd: "/reopened", directories: [] }));
      await flush();
      const afterReopen = snapshot();

      input.value = "/destroyed";
      controller.validate(form, { delay: 0 });
      runNextTimer();
      const destroyedRequest = requests.shift();
      list.hidden = false;
      input.setAttribute("aria-expanded", "true");
      controller.destroy(form);
      destroyedRequest.resolve(response({ valid: false, error: "Destroyed response won", directories: [] }));
      await flush();
      const afterDestroy = snapshot();
      controller.initialize(form);

      console.log(JSON.stringify({
        listenerCounts,
        cancelledTimerCount,
        oldAborted: oldRequest.options.signal.aborted,
        closedAborted: closedRequest.options.signal.aborted,
        destroyedAborted: destroyedRequest.options.signal.aborted,
        afterNew,
        afterOld,
        afterReopen,
        afterDestroy,
        finalListenerCounts: Object.fromEntries(["input", "change", "click", "focusout", "keydown"].map((type) => [type, form.listenerCount(type)]))
      }));

      function field(selector) { return new FakeElement("div", [selector]); }
      function runNextTimer() {
        const [id, callback] = timers.entries().next().value;
        timers.delete(id);
        callback();
      }
      function response(payload) { return { ok: true, json: async () => payload }; }
      async function flush() { for (let i = 0; i < 4; i += 1) await Promise.resolve(); }
      function snapshot() {
        return {
          hiddenCwd: hidden.value || "",
          status: status.textContent,
          valid: status.classList.contains("is-valid"),
          invalid: status.classList.contains("is-invalid"),
          submitDisabled: submit.disabled,
          suggestionsHidden: list.hidden,
          expanded: input.getAttribute("aria-expanded")
        };
      }
    JS

    assert_equal({ "input" => 1, "change" => 1, "click" => 1, "focusout" => 1, "keydown" => 1 }, results.fetch("listenerCounts"))
    assert_equal 0, results.fetch("cancelledTimerCount")
    assert_equal true, results.fetch("oldAborted")
    assert_equal true, results.fetch("closedAborted")
    assert_equal true, results.fetch("destroyedAborted")
    assert_equal "/new", results.dig("afterNew", "hiddenCwd")
    assert_equal "Directory exists.", results.dig("afterNew", "status")
    assert_equal true, results.dig("afterNew", "valid")
    assert_equal false, results.dig("afterNew", "submitDisabled")
    assert_equal results.fetch("afterNew"), results.fetch("afterOld")
    assert_equal "/reopened", results.dig("afterReopen", "hiddenCwd")
    assert_equal "Directory exists.", results.dig("afterReopen", "status")
    assert_equal "Checking…", results.dig("afterDestroy", "status")
    assert_equal false, results.dig("afterDestroy", "invalid")
    assert_equal true, results.dig("afterDestroy", "suggestionsHidden")
    assert_equal "false", results.dig("afterDestroy", "expanded")
    assert_equal results.fetch("listenerCounts"), results.fetch("finalListenerCounts")
  end

  def test_sidebar_controller_replaces_and_rebinds_replaceable_sidebar_state
    results = run_javascript(<<~JS)
      const { SidebarController } = await import(#{module_url("sidebar_controller.js").to_json});

      const oldScroll = scrollElement(37);
      const newScroll = scrollElement(0);
      const oldSidebar = sidebar(oldScroll, "Old title");
      const newSidebar = sidebar(newScroll, "New title");
      const mobileButton = element();
      let currentSidebar = oldSidebar;
      Object.defineProperty(oldSidebar, "outerHTML", { set() { currentSidebar = newSidebar; } });
      const events = [];
      const document = eventTarget({
        body: element(), activeElement: null,
        querySelector(selector) {
          if (selector === ".session-sidebar") return currentSidebar;
          if (selector === "[data-modal]:not([hidden])") return null;
          return null;
        },
        querySelectorAll: (selector) => selector === ".mobile-sessions-button" ? [mobileButton] : [],
        createElement: () => element(),
        getElementById: () => null,
        dispatchEvent: (event) => events.push([event.type, event.detail])
      });
      const window = eventTarget({
        location: { href: "https://example.test/?session=one", origin: "https://example.test" },
        history: { state: null, pushState() {} },
        CustomEvent: class { constructor(type, options) { this.type = type; this.detail = options.detail; } }
      });
      const projectCalls = [];
      const projectSelect = {
        initialize(root) { projectCalls.push(["initialize", root.name]); },
        destroy(root) { projectCalls.push(["destroy", root.name]); },
        isActive() { return false; }
      };
      const gatewayUpdate = { applications: 0, apply() { this.applications += 1; } };
      const controller = new SidebarController(document, window, projectSelect, gatewayUpdate, () => {});

      controller.initialize();
      controller.replace("<aside>replacement</aside>");
      newScroll.onscroll();

      console.log(JSON.stringify({
        projectCalls,
        gatewayApplications: gatewayUpdate.applications,
        scrollTop: newScroll.scrollTop,
        reboundScroll: typeof newScroll.onscroll === "function",
        replacementEvent: events.at(-1),
        interactionTracked: controller.recentlyInteracted(),
        badgeText: mobileButton.children[0]?.textContent || null
      }));

      function eventTarget(properties = {}) { return Object.assign({ addEventListener() {} }, properties); }
      function element() {
        const classes = new Set();
        return {
          children: [], dataset: {}, hidden: false, textContent: "",
          classList: { add: (name) => classes.add(name), remove: (name) => classes.delete(name), contains: (name) => classes.has(name), toggle: (name, enabled) => enabled ? classes.add(name) : classes.delete(name) },
          append(child) { this.children.push(child); }, remove() {}, setAttribute() {}, querySelector() { return null; }
        };
      }
      function scrollElement(scrollTop) { return Object.assign(element(), { scrollTop }); }
      function sidebar(scroll, title) {
        const result = element();
        result.name = title.startsWith("Old") ? "old" : "new";
        result.dataset.unreadSessionCount = "3";
        const selectedTitle = { textContent: title };
        result.querySelector = (selector) => {
          if (selector === ".session-sidebar-content") return scroll;
          if (selector === "a.session.selected .session-title") return selectedTitle;
          return null;
        };
        result.querySelectorAll = () => [];
        return result;
      }
    JS

    assert_equal [["initialize", "old"], ["destroy", "old"], ["initialize", "new"]], results.fetch("projectCalls")
    assert_equal 2, results.fetch("gatewayApplications")
    assert_equal 37, results.fetch("scrollTop")
    assert_equal true, results.fetch("reboundScroll")
    assert_equal ["gripi:sidebar-selected-title", { "title" => "New title" }], results.fetch("replacementEvent")
    assert_equal true, results.fetch("interactionTracked")
    assert_equal "3", results.fetch("badgeText")
  end

  def test_sidebar_controller_restores_focus_to_a_replaced_pin_button
    results = run_javascript(<<~JS)
      const { SidebarController } = await import(#{module_url("sidebar_controller.js").to_json});
      const oldPin = { dataset: { sessionPath: "/session" }, closest: (selector) => selector === "[data-session-pin-toggle]" ? oldPin : null };
      const newPin = { dataset: { sessionPath: "/session" }, focused: false, focus() { this.focused = true; }, closest: (selector) => selector === "[data-session-pin-toggle]" ? newPin : null };
      const fallback = { focused: false, focus() { this.focused = true; } };
      const oldSidebar = sidebar(oldPin);
      const newSidebar = sidebar(newPin);
      const filteredSidebar = sidebar(null, fallback);
      let currentSidebar = oldSidebar;
      Object.defineProperty(oldSidebar, "outerHTML", { set() { currentSidebar = newSidebar; } });
      Object.defineProperty(newSidebar, "outerHTML", { set() { currentSidebar = filteredSidebar; } });
      const document = {
        activeElement: oldPin, body: { classList: { contains: () => false } },
        addEventListener() {},
        querySelector: (selector) => selector === ".session-sidebar" ? currentSidebar : null,
        querySelectorAll: () => [], getElementById: () => null, dispatchEvent() {}
      };
      const window = {
        location: { href: "https://example.test/", origin: "https://example.test", search: "" },
        CustomEvent: class {}, addEventListener() {}
      };
      const controller = new SidebarController(document, window, { initialize() {}, destroy() {}, isActive: () => false }, { apply() {} }, () => {});
      controller.bind();

      controller.replace("<aside>replacement</aside>");
      document.activeElement = newPin;
      controller.replace("<aside>filtered replacement</aside>");

      console.log(JSON.stringify({ pinFocused: newPin.focused, fallbackFocused: fallback.focused }));

      function sidebar(pin, fallback = null) {
        return {
          dataset: {},
          querySelector(selector) {
            if (selector === ".session-sidebar-content") return { scrollTop: 0 };
            if (selector === "[data-sidebar-search-toggle]") return fallback;
            return null;
          },
          querySelectorAll: (selector) => selector === "[data-session-pin-toggle]" && pin ? [pin] : []
        };
      }
    JS

    assert_equal true, results.fetch("pinFocused")
    assert_equal true, results.fetch("fallbackFocused")
  end

  def test_sidebar_controller_retries_refresh_after_a_transient_failure
    results = run_javascript(<<~JS)
      const { SidebarController } = await import(#{module_url("sidebar_controller.js").to_json});
      const sidebar = {
        dataset: {}, querySelector: (selector) => selector === ".session-sidebar-content" ? { scrollTop: 0 } : null,
        querySelectorAll: () => []
      };
      const document = {
        hidden: false, activeElement: null, body: { classList: { contains: () => false } },
        addEventListener() {}, querySelector: (selector) => selector === ".session-sidebar" ? sidebar : null,
        querySelectorAll: () => [], createElement: () => ({}), getElementById: () => null, dispatchEvent() {}
      };
      const window = {
        location: { href: "https://example.test/?session=one", origin: "https://example.test" },
        addEventListener() {}, CustomEvent: class {}
      };
      const timers = [];
      globalThis.setTimeout = (_callback, delay) => { timers.push(delay); return timers.length; };
      globalThis.clearTimeout = () => {};
      globalThis.fetch = async () => ({ ok: false });
      const controller = new SidebarController(document, window, { initialize() {}, isActive: () => false }, { apply() {} }, () => {});
      controller.initialize();

      await controller.refresh();
      console.log(JSON.stringify(timers));
    JS

    assert_equal [10_000], results
  end

  def test_sidebar_controller_defers_regular_refresh_after_recent_interaction
    results = run_javascript(<<~JS)
      const { SidebarController } = await import(#{module_url("sidebar_controller.js").to_json});
      const sidebar = { dataset: {}, querySelector: () => null, querySelectorAll: () => [] };
      const document = {
        hidden: false, activeElement: null, body: { classList: { contains: () => false } },
        addEventListener() {}, querySelector: (selector) => selector === ".session-sidebar" ? sidebar : null,
        querySelectorAll: () => [], getElementById: () => null
      };
      const window = { location: { href: "https://example.test/", origin: "https://example.test", search: "" } };
      const timers = [];
      let requests = 0;
      globalThis.setTimeout = (_callback, delay) => { timers.push(delay); return timers.length; };
      globalThis.clearTimeout = () => {};
      globalThis.fetch = async () => { requests += 1; return { ok: true }; };
      const controller = new SidebarController(document, window, { initialize() {}, isActive: () => false }, { apply() {} }, () => {});
      controller.bind();
      controller.lastInteractionAt = Date.now();

      await controller.refresh();

      console.log(JSON.stringify({ timers, requests }));
    JS

    assert_equal [1_000], results.fetch("timers")
    assert_equal 0, results.fetch("requests")
  end

  def test_sidebar_controller_refreshes_more_often_while_a_session_is_active
    results = run_javascript(<<~JS)
      const { SidebarController } = await import(#{module_url("sidebar_controller.js").to_json});
      let active = false;
      const sidebar = {
        dataset: {},
        querySelector(selector) {
          if (selector.includes("session-running-indicator")) return active ? {} : null;
          return null;
        },
        querySelectorAll: () => []
      };
      const document = {
        hidden: false, body: { classList: { contains: () => false } },
        addEventListener() {}, querySelector: (selector) => selector === ".session-sidebar" ? sidebar : null,
        querySelectorAll: () => [], createElement: () => ({}), getElementById: () => null
      };
      const window = { location: { href: "https://example.test/", origin: "https://example.test" } };
      const timers = [];
      globalThis.setTimeout = (_callback, delay) => { timers.push(delay); return timers.length; };
      globalThis.clearTimeout = () => {};
      const controller = new SidebarController(document, window, { initialize() {} }, { apply() {} }, () => {});
      controller.bind();

      controller.scheduleRefresh();
      active = true;
      controller.scheduleRefresh();
      controller.requestRefresh();

      console.log(JSON.stringify(timers));
    JS

    assert_equal [10_000, 2_500, 0], results
  end

  def test_sidebar_controller_shows_loading_state_while_toggling_pins
    results = run_javascript(<<~JS)
      const { SidebarController } = await import(#{module_url("sidebar_controller.js").to_json});
      const sidebar = { dataset: {}, querySelector: () => null, querySelectorAll: () => [] };
      const document = {
        hidden: false, activeElement: null, body: { classList: { contains: () => false } },
        addEventListener() {}, querySelector: (selector) => selector === ".session-sidebar" ? sidebar : null,
        querySelectorAll: () => [], getElementById: () => null
      };
      const window = { location: { href: "https://example.test/", origin: "https://example.test", search: "" } };
      const timers = [];
      const requests = [];
      const sidebarUrls = [];
      let sidebarRequests = 0;
      globalThis.setTimeout = (_callback, delay) => { timers.push(delay); return timers.length; };
      globalThis.clearTimeout = () => {};
      const controller = new SidebarController(document, window, { initialize() {}, destroy() {}, isActive: () => false }, { apply() {} }, () => {});
      controller.bind();
      controller.lastInteractionAt = Date.now();

      const pin = await toggle("false", true);
      const unpin = await toggle("true", false);
      console.log(JSON.stringify({ requests, sidebarRequests, sidebarUrls, timers, pin, unpin }));

      async function toggle(initiallyPinned, resultingPinned) {
        let resolvePin;
        let resolveSidebar;
        globalThis.fetch = (url, options) => {
          if (String(url) === "/sessions/pin") {
            requests.push([url, options.method, Object.fromEntries(options.body)]);
            return new Promise((resolve) => { resolvePin = () => resolve({ ok: true, json: async () => ({ session: "/session", pinned: resultingPinned }) }); });
          }
          sidebarRequests += 1;
          sidebarUrls.push(String(url));
          return new Promise((resolve) => { resolveSidebar = () => resolve({ ok: true, text: async () => "<aside>refreshed</aside>" }); });
        };
        const button = pinButton(initiallyPinned);
        const pending = controller.togglePin(button);
        const loading = state(button);
        await new Promise((resolve) => setImmediate(resolve));
        resolvePin();
        await new Promise((resolve) => setImmediate(resolve));
        const awaitingSidebar = state(button);
        const sidebarStarted = !!resolveSidebar;
        resolveSidebar?.();
        const payload = await pending;
        return { payload, loading, awaitingSidebar, sidebarStarted, settled: state(button), pinned: button.dataset.pinned };
      }

      function state(button) {
        return {
          disabled: button.disabled, className: button.className, busy: button.attributes["aria-busy"],
          label: button.attributes["aria-label"], title: button.attributes.title, pressed: button.attributes["aria-pressed"]
        };
      }

      function pinButton(pinned) {
        const idleLabel = pinned === "true" ? "Unpin session" : "Pin session";
        const classes = new Set(["session-pin-toggle", ...(pinned === "true" ? ["is-pinned"] : [])]);
        return {
          disabled: false, dataset: { sessionPath: "/session", pinned },
          attributes: { "aria-label": idleLabel, "aria-pressed": pinned, title: idleLabel },
          get className() { return [...classes].join(" "); },
          classList: {
            add: (name) => classes.add(name), remove: (name) => classes.delete(name),
            toggle: (name, enabled) => enabled ? classes.add(name) : classes.delete(name)
          },
          setAttribute(name, value) { this.attributes[name] = String(value); },
          removeAttribute(name) { delete this.attributes[name]; }
        };
      }
    JS

    assert_equal [
      ["/sessions/pin", "POST", { "session" => "/session", "pinned" => "true" }],
      ["/sessions/pin", "POST", { "session" => "/session", "pinned" => "false" }]
    ], results.fetch("requests")
    assert_equal 2, results.fetch("sidebarRequests")
    assert_equal ["https://example.test/sidebar", "https://example.test/sidebar"], results.fetch("sidebarUrls")
    assert_equal [10_000, 10_000], results.fetch("timers")
    assert_equal({
      "payload" => { "session" => "/session", "pinned" => true },
      "loading" => { "disabled" => true, "className" => "session-pin-toggle is-loading", "busy" => "true", "label" => "Pinning session", "title" => "Pinning session", "pressed" => "false" },
      "awaitingSidebar" => { "disabled" => true, "className" => "session-pin-toggle is-loading is-pinned", "busy" => "true", "label" => "Pinning session", "title" => "Pinning session", "pressed" => "true" },
      "sidebarStarted" => true,
      "settled" => { "disabled" => false, "className" => "session-pin-toggle is-pinned", "label" => "Unpin session", "title" => "Unpin session", "pressed" => "true" },
      "pinned" => "true"
    }, results.fetch("pin"))
    assert_equal({
      "payload" => { "session" => "/session", "pinned" => false },
      "loading" => { "disabled" => true, "className" => "session-pin-toggle is-pinned is-loading", "busy" => "true", "label" => "Unpinning session", "title" => "Unpinning session", "pressed" => "true" },
      "awaitingSidebar" => { "disabled" => true, "className" => "session-pin-toggle is-loading", "busy" => "true", "label" => "Unpinning session", "title" => "Unpinning session", "pressed" => "false" },
      "sidebarStarted" => true,
      "settled" => { "disabled" => false, "className" => "session-pin-toggle", "label" => "Pin session", "title" => "Pin session", "pressed" => "false" },
      "pinned" => "false"
    }, results.fetch("unpin"))
  end

  def test_sidebar_controller_resumes_periodic_refresh_after_pin_failure
    results = run_javascript(<<~JS)
      const { SidebarController } = await import(#{module_url("sidebar_controller.js").to_json});
      const sidebar = { dataset: {}, querySelector: () => null, querySelectorAll: () => [] };
      const document = {
        hidden: false, activeElement: null, body: { classList: { contains: () => false } },
        addEventListener() {}, querySelector: (selector) => selector === ".session-sidebar" ? sidebar : null,
        querySelectorAll: () => [], getElementById: () => null
      };
      const window = { location: { href: "https://example.test/", origin: "https://example.test", search: "" } };
      const timers = [];
      globalThis.setTimeout = (_callback, delay) => { timers.push(delay); return timers.length; };
      globalThis.clearTimeout = () => {};
      globalThis.fetch = async () => ({ ok: false });
      const controller = new SidebarController(document, window, { initialize() {}, isActive: () => false }, { apply() {} }, () => {});
      controller.bind();
      const classes = new Set(["session-pin-toggle", "is-pinned"]);
      const button = {
        disabled: false, dataset: { sessionPath: "/session", pinned: "true" },
        attributes: { "aria-label": "Unpin session", "aria-pressed": "true", title: "Unpin session" },
        get className() { return [...classes].join(" "); },
        classList: { add: (name) => classes.add(name), remove: (name) => classes.delete(name) },
        setAttribute(name, value) { this.attributes[name] = String(value); },
        removeAttribute(name) { delete this.attributes[name]; }
      };

      try { await controller.togglePin(button); } catch (_error) {}

      console.log(JSON.stringify({ timers, disabled: button.disabled, className: button.className, busy: button.attributes["aria-busy"], label: button.attributes["aria-label"], title: button.attributes.title, pressed: button.attributes["aria-pressed"], pinned: button.dataset.pinned }));
    JS

    assert_equal [10_000], results.fetch("timers")
    assert_equal false, results.fetch("disabled")
    assert_equal "session-pin-toggle is-pinned", results.fetch("className")
    assert_nil results["busy"]
    assert_equal "Unpin session", results.fetch("label")
    assert_equal "Unpin session", results.fetch("title")
    assert_equal "true", results.fetch("pressed")
    assert_equal "true", results.fetch("pinned")
  end

  def test_sidebar_refresh_failure_cleans_up_pin_operation
    results = run_javascript(<<~JS)
      const { SidebarController } = await import(#{module_url("sidebar_controller.js").to_json});
      const sidebar = { dataset: {}, querySelector: () => null, querySelectorAll: () => [] };
      const document = {
        hidden: false, activeElement: null, body: { classList: { contains: () => false } },
        addEventListener() {}, querySelector: (selector) => selector === ".session-sidebar" ? sidebar : null,
        querySelectorAll: () => [], getElementById: () => null
      };
      const window = { location: { href: "https://example.test/", origin: "https://example.test", search: "" } };
      const timers = [];
      let postRequests = 0;
      globalThis.setTimeout = (_callback, delay) => { timers.push(delay); return timers.length; };
      globalThis.clearTimeout = () => {};
      globalThis.fetch = async (url) => {
        if (String(url) !== "/sessions/pin") return { ok: false };
        postRequests += 1;
        return { ok: true, json: async () => ({ session: "/session", pinned: true }) };
      };
      const controller = new SidebarController(document, window, { initialize() {}, isActive: () => false }, { apply() {} }, () => {});
      controller.bind();

      const firstButton = button();
      await controller.togglePin(firstButton);
      const secondButton = button();
      await controller.togglePin(secondButton);

      console.log(JSON.stringify({ postRequests, timers, firstLoading: firstButton.loading, secondLoading: secondButton.loading, operationActive: controller.pinOperationActive }));

      function button() {
        const classes = new Set();
        return {
          disabled: false, dataset: { sessionPath: "/session", pinned: "false" },
          get loading() { return classes.has("is-loading"); },
          classList: {
            add: (name) => classes.add(name), remove: (name) => classes.delete(name),
            toggle: (name, enabled) => enabled ? classes.add(name) : classes.delete(name)
          },
          setAttribute() {}, removeAttribute() {}
        };
      }
    JS

    assert_equal 2, results.fetch("postRequests")
    assert_equal [10_000, 10_000], results.fetch("timers")
    assert_equal false, results.fetch("firstLoading")
    assert_equal false, results.fetch("secondLoading")
    assert_equal false, results.fetch("operationActive")
  end

  def test_successful_pin_requests_refresh_a_replaced_sidebar
    results = run_javascript(<<~JS)
      const { SidebarController } = await import(#{module_url("sidebar_controller.js").to_json});
      const first = { dataset: {}, querySelector: () => null, querySelectorAll: () => [] };
      const second = { dataset: {}, querySelector: () => null, querySelectorAll: () => [] };
      let current = first;
      const document = {
        hidden: false, activeElement: null, body: { classList: { contains: () => false } },
        addEventListener() {}, querySelector: (selector) => selector === ".session-sidebar" ? current : null,
        querySelectorAll: () => [], getElementById: () => null
      };
      const window = { location: { href: "https://example.test/", origin: "https://example.test", search: "" } };
      const timers = [];
      globalThis.setTimeout = (_callback, delay) => { timers.push(delay); return timers.length; };
      globalThis.clearTimeout = () => {};
      let resolveRequest;
      globalThis.fetch = (url) => {
        if (String(url) !== "/sessions/pin") return Promise.resolve({ ok: true, text: async () => "<aside>refreshed</aside>" });
        return new Promise((resolve) => { resolveRequest = () => resolve({ ok: true, json: async () => ({ session: "/session", pinned: true }) }); });
      };
      const controller = new SidebarController(document, window, { initialize() {}, destroy() {}, isActive: () => false }, { apply() {} }, () => {});
      controller.bind();
      const classes = new Set();
      const successMutations = [];
      const button = {
        disabled: false, dataset: { sessionPath: "/session", pinned: "false" },
        get className() { return [...classes].join(" "); },
        classList: {
          add: (name) => classes.add(name),
          remove: (name) => { classes.delete(name); successMutations.push(["remove-class", name]); },
          toggle: (name, enabled) => successMutations.push(["class", name, enabled])
        },
        setAttribute(name, value) { successMutations.push(["attribute", name, value]); },
        removeAttribute(name) { successMutations.push(["remove-attribute", name]); }
      };

      const pending = controller.togglePin(button);
      current = second;
      controller.bind();
      button.isConnected = false;
      successMutations.length = 0;
      await new Promise((resolve) => setImmediate(resolve));
      resolveRequest();
      const payload = await pending;

      console.log(JSON.stringify({ payload, timers, boundToReplacement: controller.element === second, successMutations, stalePinned: button.dataset.pinned, staleDisabled: button.disabled, staleClassName: button.className }));
    JS

    assert_equal({ "session" => "/session", "pinned" => true }, results.fetch("payload"))
    assert_equal [10_000], results.fetch("timers")
    assert_equal true, results.fetch("boundToReplacement")
    assert_empty results.fetch("successMutations")
    assert_equal "false", results.fetch("stalePinned")
    assert_equal true, results.fetch("staleDisabled")
    assert_equal "is-loading", results.fetch("staleClassName")
  end

  def test_pin_spinner_owns_refresh_until_sidebar_replacement
    results = run_javascript(<<~JS)
      const { SidebarController } = await import(#{module_url("sidebar_controller.js").to_json});
      const sidebar = { dataset: {}, querySelector: () => null, querySelectorAll: () => [] };
      const replacementSidebar = { dataset: {}, querySelector: () => null, querySelectorAll: () => [] };
      let currentSidebar = sidebar;
      const document = {
        hidden: false, activeElement: null, body: { classList: { contains: () => false } },
        addEventListener() {}, querySelector: (selector) => selector === ".session-sidebar" ? currentSidebar : null,
        querySelectorAll: () => [], getElementById: () => null, dispatchEvent() {}
      };
      const window = {
        location: { href: "https://example.test/", origin: "https://example.test", search: "" },
        CustomEvent: class {}, addEventListener() {}
      };
      const timers = [];
      const timerCallbacks = [];
      globalThis.setTimeout = (callback, delay) => { timers.push(delay); timerCallbacks.push(callback); return timers.length; };
      globalThis.clearTimeout = () => {};
      const sidebarResolvers = [];
      let resolvePin;
      let sidebarRequests = 0;
      globalThis.fetch = (url) => {
        if (String(url) === "/sessions/pin") return new Promise((resolve) => { resolvePin = () => resolve({ ok: true, json: async () => ({ session: "/session", pinned: true }) }); });
        sidebarRequests += 1;
        return new Promise((resolve) => sidebarResolvers.push(() => resolve({ ok: true, text: async () => "<aside>refreshed</aside>" })));
      };
      const controller = new SidebarController(document, window, { initialize() {}, destroy() {}, isActive: () => false }, { apply() {} }, () => {});
      controller.bind();
      const classes = new Set();
      const attributes = {};
      const button = {
        disabled: false, isConnected: true, dataset: { sessionPath: "/session", pinned: "false" },
        get loading() { return classes.has("is-loading"); },
        classList: {
          add: (name) => classes.add(name), remove: (name) => classes.delete(name),
          toggle: (name, enabled) => enabled ? classes.add(name) : classes.delete(name)
        },
        setAttribute(name, value) { attributes[name] = String(value); },
        removeAttribute(name) { delete attributes[name]; }
      };
      Object.defineProperty(sidebar, "outerHTML", { set() { currentSidebar = replacementSidebar; button.isConnected = false; } });

      const pending = controller.togglePin(button);
      controller.requestRefresh();
      await timerCallbacks[0]();
      resolvePin();
      await new Promise((resolve) => setImmediate(resolve));
      controller.requestRefresh();
      await timerCallbacks[2]();
      const loadingWhileRefreshPending = button.loading;
      sidebarResolvers.shift()();
      await pending;

      console.log(JSON.stringify({
        sidebarRequests, timers, loadingWhileRefreshPending, oldButtonStillLoading: button.loading,
        oldButtonDetached: !button.isConnected, replacementBound: controller.element === replacementSidebar
      }));
    JS

    assert_equal 1, results.fetch("sidebarRequests")
    assert_equal [0, 1_000, 0, 1_000, 10_000], results.fetch("timers")
    assert_equal true, results.fetch("loadingWhileRefreshPending")
    assert_equal true, results.fetch("oldButtonStillLoading")
    assert_equal true, results.fetch("oldButtonDetached")
    assert_equal true, results.fetch("replacementBound")
  end

  def test_sidebar_controller_ignores_concurrent_pin_toggles
    results = run_javascript(<<~JS)
      const { SidebarController } = await import(#{module_url("sidebar_controller.js").to_json});
      const sidebar = { dataset: {}, querySelector: () => null, querySelectorAll: () => [] };
      const document = {
        hidden: false, activeElement: null, body: { classList: { contains: () => false } },
        addEventListener() {}, querySelector: (selector) => selector === ".session-sidebar" ? sidebar : null,
        querySelectorAll: () => [], getElementById: () => null, dispatchEvent() {}
      };
      const window = {
        location: { href: "https://example.test/", origin: "https://example.test", search: "" },
        CustomEvent: class {}, addEventListener() {}
      };
      globalThis.setTimeout = () => 1;
      globalThis.clearTimeout = () => {};
      const postResolvers = [];
      const postPaths = [];
      let resolveSidebar;
      let sidebarRequests = 0;
      globalThis.fetch = (url, options) => {
        if (String(url) !== "/sessions/pin") {
          sidebarRequests += 1;
          return new Promise((resolve) => { resolveSidebar = () => resolve({ ok: true, text: async () => "<aside>refreshed</aside>" }); });
        }
        const body = Object.fromEntries(options.body);
        postPaths.push(body.session);
        return new Promise((resolve) => postResolvers.push(() => resolve({ ok: true, json: async () => ({ session: body.session, pinned: true }) })));
      };
      const controller = new SidebarController(document, window, { initialize() {}, destroy() {}, isActive: () => false }, { apply() {} }, () => {});
      controller.bind();
      const firstButton = button("/first");
      const secondButton = button("/second");
      const thirdButton = button("/third");

      const first = controller.togglePin(firstButton);
      const second = controller.togglePin(secondButton);
      const postsWhileFirstPending = postPaths.length;
      const secondLoading = secondButton.loading;
      postResolvers.shift()();
      await new Promise((resolve) => setImmediate(resolve));
      const third = controller.togglePin(thirdButton);
      const postsWhileRefreshPending = postPaths.length;
      const thirdLoading = thirdButton.loading;
      resolveSidebar();
      const [_firstResult, secondResult, thirdResult] = await Promise.all([first, second, third]);

      console.log(JSON.stringify({ postPaths, postsWhileFirstPending, postsWhileRefreshPending, sidebarRequests, secondLoading, thirdLoading, secondResult, thirdResult }));

      function button(path) {
        const classes = new Set();
        return {
          disabled: false, dataset: { sessionPath: path, pinned: "false" },
          get loading() { return classes.has("is-loading"); },
          classList: {
            add: (name) => classes.add(name), remove: (name) => classes.delete(name),
            toggle: (name, enabled) => enabled ? classes.add(name) : classes.delete(name)
          },
          setAttribute() {}, removeAttribute() {}
        };
      }
    JS

    assert_equal ["/first"], results.fetch("postPaths")
    assert_equal 1, results.fetch("postsWhileFirstPending")
    assert_equal 1, results.fetch("postsWhileRefreshPending")
    assert_equal 1, results.fetch("sidebarRequests")
    assert_equal false, results.fetch("secondLoading")
    assert_equal false, results.fetch("thirdLoading")
    assert_nil results["secondResult"]
    assert_nil results["thirdResult"]
  end

  def test_marking_compaction_replaces_the_idle_refresh_schedule
    results = run_javascript(<<~JS)
      const { SidebarController } = await import(#{module_url("sidebar_controller.js").to_json});
      let compactingIndicator = null;
      const indicators = {
        querySelector: (selector) => selector === ".session-compacting-indicator" ? compactingIndicator : null,
        appendChild(indicator) { compactingIndicator = indicator; }
      };
      const link = {
        classList: { add() {} },
        querySelector: (selector) => selector === ".session-indicators" ? indicators : null
      };
      const sidebar = {
        dataset: {},
        querySelector(selector) {
          if (selector.startsWith("a.session[data-session-path=")) return link;
          if (selector.includes("session-compacting-indicator")) return compactingIndicator;
          return null;
        },
        querySelectorAll: () => []
      };
      const document = {
        hidden: false, body: { classList: { contains: () => false } },
        addEventListener() {}, querySelector: (selector) => selector === ".session-sidebar" ? sidebar : null,
        querySelectorAll: () => [], createElement: () => ({ setAttribute() {} }), getElementById: () => null
      };
      const window = { location: { href: "https://example.test/", origin: "https://example.test" } };
      const timers = [];
      globalThis.CSS = { escape: (value) => value };
      globalThis.setTimeout = (_callback, delay) => { timers.push(delay); return timers.length; };
      globalThis.clearTimeout = () => {};
      const controller = new SidebarController(document, window, { initialize() {} }, { apply() {} }, () => {});
      controller.bind();

      controller.scheduleRefresh();
      controller.markSessionCompacting("/session");

      console.log(JSON.stringify({ timers, marked: compactingIndicator !== null }));
    JS

    assert_equal [10_000, 2_500], results.fetch("timers")
    assert_equal true, results.fetch("marked")
  end

  def test_requested_sidebar_refresh_invalidates_an_in_flight_refresh
    results = run_javascript(<<~JS)
      const { SidebarController } = await import(#{module_url("sidebar_controller.js").to_json});
      const sidebar = {
        dataset: {}, replacements: 0,
        querySelector: () => null, querySelectorAll: () => [],
        set outerHTML(_html) { this.replacements += 1; }
      };
      const document = {
        hidden: false, activeElement: null, body: { classList: { contains: () => false } },
        addEventListener() {}, querySelector(selector) {
          if (selector === ".session-sidebar") return sidebar;
          if (selector === "[data-modal]:not([hidden])") return null;
          return null;
        },
        querySelectorAll: () => [], createElement: () => ({}), getElementById: () => null
      };
      const window = { location: { href: "https://example.test/", origin: "https://example.test" } };
      const timers = [];
      globalThis.setTimeout = (_callback, delay) => { timers.push(delay); return timers.length; };
      globalThis.clearTimeout = () => {};
      let resolveRequest;
      globalThis.fetch = () => new Promise((resolve) => { resolveRequest = resolve; });
      const controller = new SidebarController(document, window, { initialize() {}, isActive: () => false }, { apply() {} }, () => {});
      controller.bind();

      const staleRefresh = controller.refresh();
      controller.requestRefresh();
      resolveRequest({ ok: true, text: async () => "stale html" });
      await staleRefresh;

      console.log(JSON.stringify({ timers, replacements: sidebar.replacements }));
    JS

    assert_equal [0], results.fetch("timers")
    assert_equal 0, results.fetch("replacements")
  end

  def test_sidebar_controller_ignores_stale_refresh_responses_after_rebinding
    results = run_javascript(<<~JS)
      const { SidebarController } = await import(#{module_url("sidebar_controller.js").to_json});

      const first = sidebar("first");
      const second = sidebar("second");
      let current = first;
      const document = {
        hidden: false, activeElement: null, body: { classList: { contains: () => false } },
        addEventListener() {}, querySelector(selector) {
          if (selector === ".session-sidebar") return current;
          if (selector === "[data-modal]:not([hidden])") return null;
          return null;
        },
        querySelectorAll: () => [], createElement: () => ({}), getElementById: () => null,
        dispatchEvent() {}
      };
      const window = {
        location: { href: "https://example.test/?session=one", origin: "https://example.test" },
        addEventListener() {}, CustomEvent: class {}
      };
      let resolveRequest;
      globalThis.fetch = () => new Promise((resolve) => { resolveRequest = resolve; });
      const projectSelect = { initialize() {}, destroy() {}, isActive: () => false };
      const controller = new SidebarController(document, window, projectSelect, { apply() {} }, () => {});
      controller.initialize();

      const refresh = controller.refresh();
      current = second;
      controller.bind();
      resolveRequest({ ok: true, text: async () => "stale html" });
      await refresh;

      console.log(JSON.stringify({ firstReplacements: first.replacements, secondReplacements: second.replacements, bound: controller.element.name }));

      function sidebar(name) {
        return {
          name, dataset: {}, replacements: 0,
          querySelector(selector) { return selector === ".session-sidebar-content" ? { scrollTop: 0 } : null; },
          querySelectorAll() { return []; },
          set outerHTML(_html) { this.replacements += 1; }
        };
      }
    JS

    assert_equal 0, results.fetch("firstReplacements")
    assert_equal 0, results.fetch("secondReplacements")
    assert_equal "second", results.fetch("bound")
  end

  private

  def dom_fakes
    <<~JS
      class FakeEventTarget {
        constructor() { this.listeners = new Map(); }
        addEventListener(type, listener) {
          const listeners = this.listeners.get(type) || [];
          listeners.push(listener);
          this.listeners.set(type, listeners);
        }
        removeEventListener(type, listener) {
          this.listeners.set(type, (this.listeners.get(type) || []).filter((candidate) => candidate !== listener));
        }
        listenerCount(type) { return (this.listeners.get(type) || []).length; }
      }

      class FakeElement extends FakeEventTarget {
        constructor(tagName = "div", selectors = []) {
          super();
          this.tagName = tagName.toUpperCase();
          this.selectors = new Set(selectors);
          this.attributes = new Map();
          this.children = [];
          this.parentElement = null;
          this.dataset = {};
          this.hidden = false;
          this.disabled = false;
          this.tabIndex = 0;
          this.textContent = "";
          this.value = "";
          this.style = { values: new Map(), setProperty: (name, value) => this.style.values.set(name, value), removeProperty: (name) => this.style.values.delete(name) };
          this.classes = new Set();
          this.classList = {
            add: (...names) => names.forEach((name) => this.classes.add(name)),
            remove: (...names) => names.forEach((name) => this.classes.delete(name)),
            contains: (name) => this.classes.has(name),
            toggle: (name, enabled) => enabled ? this.classes.add(name) : this.classes.delete(name)
          };
        }
        set className(value) { this.classes = new Set(value.split(/\\s+/).filter(Boolean)); }
        get className() { return [...this.classes].join(" "); }
        setAttribute(name, value) { this.attributes.set(name, String(value)); }
        getAttribute(name) { return this.attributes.has(name) ? this.attributes.get(name) : null; }
        removeAttribute(name) { this.attributes.delete(name); }
        hasAttribute(name) { return this.attributes.has(name); }
        append(...children) { children.forEach((child) => { child.remove(); child.parentElement = this; this.children.push(child); }); }
        replaceChildren(...children) { this.children.forEach((child) => { child.parentElement = null; }); this.children = []; this.append(...children); }
        remove() {
          if (!this.parentElement) return;
          this.parentElement.children = this.parentElement.children.filter((child) => child !== this);
          this.parentElement = null;
        }
        matches(selector) {
          if (selector.includes(",")) return selector.split(",").some((part) => this.matches(part.trim()));
          if (this.selectors.has(selector)) return true;
          if (selector === this.tagName.toLowerCase()) return true;
          if (selector === "[hidden]") return this.hidden;
          if (selector.startsWith(".")) return this.classList.contains(selector.slice(1));
          if (selector === "[data-project-select]") return this.selectors.has(selector);
          if (selector === "[data-cwd-suggestion]") return this.dataset.cwdSuggestion !== undefined;
          return false;
        }
        closest(selector) {
          for (let element = this; element; element = element.parentElement) if (element.matches(selector)) return element;
          return null;
        }
        contains(element) { return element === this || this.children.some((child) => child.contains(element)); }
        querySelectorAll(selector) {
          return this.children.flatMap((child) => [child, ...child.querySelectorAll(selector)]).filter((child) => child.matches(selector));
        }
        querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
        focus() { document.activeElement = this; }
        scrollIntoView() {}
        getBoundingClientRect() { return { top: 0, bottom: 20, left: 0, width: 200 }; }
        dispatchEvent() { return true; }
      }

      class FakeDocument extends FakeEventTarget {
        constructor() {
          super();
          this.body = new FakeElement("body");
          this.ids = new Map();
          this.activeElement = null;
        }
        createElement(tagName) { return new FakeElement(tagName); }
        getElementById(id) { return this.ids.get(id) || null; }
        querySelectorAll(selector) { return this.body.querySelectorAll(selector); }
        querySelector(selector) { return this.body.querySelector(selector); }
      }
    JS
  end

  def module_url(name)
    "file://#{File.join(ASSETS, name)}"
  end

  def run_javascript(source)
    stdout, stderr, status = Open3.capture3("node", "--input-type=module", "-e", source)
    assert status.success?, stderr
    JSON.parse(stdout)
  end
end
