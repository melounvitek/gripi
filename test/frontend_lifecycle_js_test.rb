require "minitest/autorun"
require "json"
require "open3"

class FrontendLifecycleJsTest < Minitest::Test
  ASSETS = File.expand_path("../public/assets", __dir__)

  def test_conversation_rebinding_detaches_old_scroll_events_and_timers
    results = run_javascript(<<~JS)
      const { ConversationController } = await import(#{module_url("conversation_controller.js").to_json});
      class Target {
        constructor() { this.listeners = new Map(); }
        addEventListener(type, listener) { const list = this.listeners.get(type) || []; list.push(listener); this.listeners.set(type, list); }
        removeEventListener(type, listener) { this.listeners.set(type, (this.listeners.get(type) || []).filter((item) => item !== listener)); }
        dispatch(type) { (this.listeners.get(type) || []).forEach((listener) => listener({ key: "", preventDefault() {} })); }
        count(type) { return (this.listeners.get(type) || []).length; }
      }
      function scroll(name) {
        return Object.assign(new Target(), {
          name, scrollTop: 20, scrollHeight: 500, clientHeight: 100, dataset: {},
          querySelectorAll() { return []; }, querySelector() { return null; },
          getBoundingClientRect() { return { top: 0, bottom: 100 }; }, scrollTo() {}
        });
      }
      const oldScroll = scroll("old");
      const newScroll = scroll("new");
      let current = oldScroll;
      const bodyClasses = new Set();
      const document = {
        body: { classList: { contains: (name) => bodyClasses.has(name), add: (name) => bodyClasses.add(name), remove: (name) => bodyClasses.delete(name) } },
        getElementById: (id) => id === "conversation-scroll" ? current : null,
        querySelector: () => null
      };
      const window = { location: { search: "", origin: "https://example.test" }, matchMedia: () => ({ matches: false }) };
      const timers = new Map();
      let timerId = 0;
      globalThis.setTimeout = (callback) => { const id = ++timerId; timers.set(id, callback); return id; };
      globalThis.clearTimeout = (id) => timers.delete(id);
      globalThis.requestAnimationFrame = () => 1;
      globalThis.cancelAnimationFrame = () => {};

      const controller = new ConversationController(document, window);
      controller.bind();
      oldScroll.scrollTop = 40;
      oldScroll.dispatch("scroll");
      const directionBefore = controller.scrollDirection;
      const oldTimerCallbacks = [...timers.values()];
      current = newScroll;
      controller.bind();
      oldScroll.scrollTop = 80;
      oldScroll.dispatch("scroll");
      oldTimerCallbacks.forEach((callback) => callback());

      console.log(JSON.stringify({
        directionBefore,
        directionAfter: controller.scrollDirection,
        oldScrollListeners: oldScroll.count("scroll"),
        newScrollListeners: newScroll.count("scroll"),
        bodyScrolling: bodyClasses.has("is-conversation-scrolling"),
        epoch: controller.bindingEpoch
      }));
    JS

    assert_equal "down", results.fetch("directionBefore")
    assert_nil results.fetch("directionAfter")
    assert_equal 0, results.fetch("oldScrollListeners")
    assert_equal 1, results.fetch("newScrollListeners")
    assert_equal false, results.fetch("bodyScrolling")
    assert_equal 2, results.fetch("epoch")
  end

  def test_session_name_feedback_is_visible_only_after_setting_a_name
    app_source = File.read(File.join(ASSETS, "app.js"))
    helper_source = app_source.match(/function appendSessionNameFeedback\(payload\) \{.*?\n\}/m).to_s
      .sub("function appendSessionNameFeedback(payload)", "globalThis.appendSessionNameFeedback = function(payload)")

    results = run_javascript(<<~JS)
      const appended = [];
      const liveMessageRenderer = {
        appendMessage(...args) { appended.push(args); }
      };
      eval(#{helper_source.to_json});

      appendSessionNameFeedback({ name: "Useful name" });
      appendSessionNameFeedback({ name: "API `v2` work" });
      appendSessionNameFeedback({ name: "Useful name", current: true });
      const [role, text, live, forceScroll, _timestamp, options] = appended[0];
      console.log(JSON.stringify({ count: appended.length, role, text, backtickText: appended[1][1], live, forceScroll, options }));
    JS

    assert_equal 2, results.fetch("count")
    assert_equal "status", results.fetch("role")
    assert_equal "Session renamed to: `Useful name`", results.fetch("text")
    assert_equal "Session renamed to: ``API `v2` work``", results.fetch("backtickText")
    assert_equal true, results.fetch("live")
    assert_equal true, results.fetch("forceScroll")
    assert_equal({ "markdown" => true }, results.fetch("options"))
  end

  def test_expanding_tool_output_starts_at_its_internal_bottom_without_moving_the_conversation
    app_source = File.read(File.join(ASSETS, "app.js"))
    handler_source = app_source.match(/document\.addEventListener\("click", \(event\) => \{\n  const button = event\.target\.closest\("\[data-tool-output-toggle\]"\);.*?\n\}\);/m).to_s

    results = run_javascript(<<~JS)
      const { activateToolOutputRegion } = await import(#{module_url("dom.js").to_json});
      let clickHandler = null;
      const document = { addEventListener(type, handler) { if (type === "click") clickHandler = handler; } };
      eval(#{handler_source.to_json});

      const conversation = { scrollTop: 70 };
      let internalScrollTop = 12;
      const body = {
        scrollHeight: 120,
        clientHeight: 100,
        get scrollTop() { return internalScrollTop; },
        set scrollTop(value) { internalScrollTop = Math.min(value, this.scrollHeight - this.clientHeight); },
        setAttribute() {},
        focus() {},
        replaceChildren() { this.scrollHeight = 300; }
      };
      const fullTemplate = { content: { cloneNode: () => ({ childNodes: [] }) } };
      const control = { hidden: false };
      const collapse = {
        dataset: { collapsed: "true" },
        querySelector(selector) {
          return {
            "[data-tool-output-body]": body,
            "[data-tool-output-full]": fullTemplate,
            "[data-tool-output-collapse-control]": control
          }[selector];
        }
      };
      const button = {
        closest: (selector) => selector === "[data-tool-output-collapse]" ? collapse : null,
        setAttribute() {}
      };

      clickHandler({ target: { closest: (selector) => selector === "[data-tool-output-toggle]" ? button : null } });
      const expandedScrollTop = body.scrollTop;
      body.scrollTop = 25;
      console.log(JSON.stringify({ expandedScrollTop, conversationScrollTop: conversation.scrollTop, userScrollTop: body.scrollTop }));
    JS

    assert_equal 200, results.fetch("expandedScrollTop")
    assert_equal 70, results.fetch("conversationScrollTop")
    assert_equal 25, results.fetch("userScrollTop")
  end

  def test_composer_focus_follows_work_lifecycle_on_desktop_only_near_the_conversation_bottom
    app_source = File.read(File.join(ASSETS, "app.js"))
    focus_source = app_source.match(/function automaticComposerFocusEnabled\(\).*?(?=\nfunction desktopConversationFocusEnabled)/m).to_s

    results = run_javascript(<<~JS)
      let modalOpen = false;
      let nearBottom = true;
      const focused = [];
      const focusTarget = (name) => ({ focus(options) { focused.push([name, options]); } });
      const window = { matchMedia: () => ({ matches: true }) };
      const document = { activeElement: null };
      const modalIsOpen = () => modalOpen;
      const conversationController = { nearBottom: () => nearBottom };
      const promptTextarea = focusTarget("prompt");
      const conversationScroll = focusTarget("conversation");
      let composerState = { dataset: { state: "idle" } };
      eval(#{(focus_source + "\nglobalThis.syncComposerFocusUnderTest = syncComposerFocus;").to_json});

      globalThis.syncComposerFocusUnderTest();
      globalThis.syncComposerFocusUnderTest("sending");
      nearBottom = false;
      globalThis.syncComposerFocusUnderTest("running");
      globalThis.syncComposerFocusUnderTest("done");
      nearBottom = true;
      globalThis.syncComposerFocusUnderTest("done");
      modalOpen = true;
      globalThis.syncComposerFocusUnderTest("running");

      console.log(JSON.stringify(focused));
    JS

    assert_equal [
      ["prompt", { "preventScroll" => true }],
      ["conversation", { "preventScroll" => true }],
      ["conversation", { "preventScroll" => true }],
      ["prompt", { "preventScroll" => true }]
    ], results
  end

  def test_composer_focus_does_not_interrupt_expanded_tool_output
    app_source = File.read(File.join(ASSETS, "app.js"))
    focus_source = app_source.match(/function automaticComposerFocusEnabled\(\).*?(?=\nfunction desktopConversationFocusEnabled)/m).to_s

    results = run_javascript(<<~JS)
      let focusCount = 0;
      const expandedOutput = { matches: (selector) => selector === '[data-tool-output-body][role="region"]' };
      const document = { activeElement: expandedOutput };
      const window = { matchMedia: () => ({ matches: true }) };
      const modalIsOpen = () => false;
      const promptTextarea = { focus() { focusCount += 1; } };
      const conversationScroll = { focus() { focusCount += 1; } };
      const composerState = { dataset: { state: "running" } };
      eval(#{(focus_source + "\nglobalThis.syncComposerFocusUnderTest = syncComposerFocus;").to_json});
      globalThis.syncComposerFocusUnderTest("done");
      console.log(JSON.stringify({ focusCount }));
    JS

    assert_equal 0, results.fetch("focusCount")
  end

  def test_composer_focus_remains_automatic_only_for_fine_pointers
    app_source = File.read(File.join(ASSETS, "app.js"))
    focus_source = app_source.match(/function automaticComposerFocusEnabled\(\).*?(?=\nfunction desktopConversationFocusEnabled)/m).to_s

    results = run_javascript(<<~JS)
      let focusCount = 0;
      const window = { matchMedia: () => ({ matches: false }) };
      const document = { activeElement: null };
      const modalIsOpen = () => false;
      const promptTextarea = { focus() { focusCount += 1; } };
      const conversationScroll = { focus() { focusCount += 1; } };
      const composerState = { dataset: { state: "running" } };
      eval(#{(focus_source + "\nglobalThis.syncComposerFocusUnderTest = syncComposerFocus;").to_json});
      globalThis.syncComposerFocusUnderTest();
      globalThis.syncComposerFocusUnderTest("idle");
      console.log(JSON.stringify({ focusCount }));
    JS

    assert_equal 0, results.fetch("focusCount")
  end

  def test_opening_session_shortcut_keeps_shortcut_mode_visible
    app_source = File.read(File.join(ASSETS, "app.js"))
    shortcut_source = app_source.match(/async function openRecentSessionShortcut\(.*?(?=\nfunction sessionShortcutsVisible)/m).to_s

    results = run_javascript(<<~JS)
      const classes = new Set(["session-shortcuts-visible"]);
      const document = { body: { classList: { contains: (name) => classes.has(name), remove: (name) => classes.delete(name) } } };
      const link = { href: "/?session=new", dataset: { sessionPath: "new" } };
      const sidebarController = { element: { querySelector: () => link } };
      const window = { location: { href: "https://example.test/?session=old" } };
      let currentSession = "old";
      const currentSessionPath = () => currentSession;
      const exitSessionShortcutMode = () => document.body.classList.remove("session-shortcuts-visible");
      const switchSession = async () => { currentSession = "new"; return true; };
      eval(#{(shortcut_source + "\nglobalThis.openRecentSessionShortcutUnderTest = openRecentSessionShortcut;").to_json});

      const switched = await globalThis.openRecentSessionShortcutUnderTest("2");
      console.log(JSON.stringify({ switched, shortcutsVisible: classes.has("session-shortcuts-visible") }));
    JS

    assert_equal true, results.fetch("switched")
    assert_equal true, results.fetch("shortcutsVisible")
  end

  def test_stale_failed_session_switch_does_not_replace_a_newer_success
    app_source = File.read(File.join(ASSETS, "app.js"))
    switch_source = app_source.match(/async function switchSession\(.*?(?=\nfunction enterSessionShortcutMode)/m).to_s
    shortcut_source = app_source.match(/async function openRecentSessionShortcut\(.*?(?=\nfunction sessionShortcutsVisible)/m).to_s

    results = run_javascript(<<~JS)
      let sessionSwitchGeneration = 0;
      let rejected;
      const firstResponse = new Promise((_resolve, reject) => { rejected = reject; });
      const responses = [firstResponse, Promise.resolve({
        ok: true,
        json: async () => ({
          sidebar_html: "sidebar", conversation_html: "conversation",
          new_session_modal_html: "", fork_session_modal_html: "",
          session: "newer", title: "Newer", url: "/?session=newer"
        })
      })];
      globalThis.fetch = () => responses.shift();
      const navigations = [];
      const window = { location: { set href(value) { navigations.push(value); } } };
      const history = { pushState() {} };
      const document = { title: "", body: { classList: { add() {}, remove() {} } } };
      const staleLink = { href: "/?session=stale", dataset: { sessionPath: "stale" } };
      const sidebarController = {
        element: { querySelector: () => staleLink }, refreshRequestVersion: 0,
        invalidate() {}, replace() {}, closeMobile() {}, scheduleRefresh() {}
      };
      let currentSession = "old";
      let conversationPanel = { set outerHTML(_value) {} };
      const sessionFragmentUrl = (url) => url;
      const persistStoredComposerDraft = () => {};
      const showSessionSwitching = () => {};
      const hideSessionSwitching = () => {};
      const resetSessionViewState = () => {};
      const replaceNewSessionModalHtml = () => {};
      const replaceForkSessionModalHtml = () => {};
      const bindSessionDom = () => { currentSession = "newer"; };
      const bindSessionControls = () => {};
      const initializeSessionView = () => {};
      const currentSessionPath = () => currentSession;
      eval(#{(switch_source + "\n" + shortcut_source + "\nglobalThis.switchSessionUnderTest = switchSession; globalThis.openRecentSessionShortcutUnderTest = openRecentSessionShortcut;").to_json});

      const staleSwitch = globalThis.openRecentSessionShortcutUnderTest("1");
      const newerResult = await globalThis.switchSessionUnderTest("/?session=newer");
      rejected(new Error("stale request failed"));
      const staleResult = await staleSwitch;

      console.log(JSON.stringify({ newerResult, staleResult, navigations, generation: sessionSwitchGeneration }));
    JS

    assert_equal true, results.fetch("newerResult")
    assert_equal false, results.fetch("staleResult")
    assert_empty results.fetch("navigations")
    assert_equal 2, results.fetch("generation")
  end

  def test_scrollend_release_listener_is_replaced_and_removed
    results = run_javascript(<<~JS)
      const { ConversationController } = await import(#{module_url("conversation_controller.js").to_json});
      class Target {
        constructor() { this.listeners = new Map(); }
        addEventListener(type, listener) { const list = this.listeners.get(type) || []; list.push(listener); this.listeners.set(type, list); }
        removeEventListener(type, listener) { this.listeners.set(type, (this.listeners.get(type) || []).filter((item) => item !== listener)); }
        dispatch(type) { [...(this.listeners.get(type) || [])].forEach((listener) => listener({})); }
        count(type) { return (this.listeners.get(type) || []).length; }
        querySelectorAll() { return []; }
        querySelector() { return null; }
      }
      const scroll = Object.assign(new Target(), { scrollTop: 0, scrollHeight: 500, clientHeight: 100, dataset: {} });
      const document = {
        body: { classList: { contains: () => false, add() {}, remove() {} } },
        getElementById: (id) => id === "conversation-scroll" ? scroll : null,
        querySelector: () => null
      };
      const window = { onscrollend: null, location: { search: "", origin: "https://example.test" }, matchMedia: () => ({ matches: false }) };
      const timers = new Map();
      let timerId = 0;
      globalThis.setTimeout = (callback, delay) => { const id = ++timerId; timers.set(id, { callback, delay }); return id; };
      globalThis.clearTimeout = (id) => timers.delete(id);
      globalThis.requestAnimationFrame = () => 1;
      globalThis.cancelAnimationFrame = () => {};

      const controller = new ConversationController(document, window);
      controller.bind();
      controller.suppressMessageJumpTargetsDuringScroll();
      controller.suppressMessageJumpTargetsDuringScroll();
      const activeAfterReplacement = scroll.count("scrollend");
      scroll.dispatch("scrollend");
      const activeAfterScrollEnd = scroll.count("scrollend");
      controller.suppressMessageJumpTargetsDuringScroll();
      [...timers.values()].find((timer) => timer.delay === 1200).callback();
      const activeAfterTimeout = scroll.count("scrollend");
      controller.suppressMessageJumpTargetsDuringScroll();
      controller.reset();

      console.log(JSON.stringify({
        activeAfterReplacement,
        activeAfterScrollEnd,
        activeAfterTimeout,
        activeAfterReset: scroll.count("scrollend"),
        bookkeeping: controller.listeners.filter((entry) => entry[1] === "scrollend").length,
        suppressed: controller.messageJumpTargetsSuppressed
      }));
    JS

    assert_equal 1, results.fetch("activeAfterReplacement")
    assert_equal 0, results.fetch("activeAfterScrollEnd")
    assert_equal 0, results.fetch("activeAfterTimeout")
    assert_equal 0, results.fetch("activeAfterReset")
    assert_equal 0, results.fetch("bookkeeping")
    assert_equal false, results.fetch("suppressed")
  end

  def test_session_sync_refreshes_external_updates_remote_takeover_and_retired_clients
    app_source = File.read(File.join(ASSETS, "app.js"))
    helper_source = app_source.match(/function sessionSyncRefreshRequired\(.*?(?=\nasync function pollEvents)/m).to_s

    results = run_javascript(<<~JS)
      let liveOutput = { dataset: { sessionSyncMode: "external_follow", sessionSyncRevision: "revision-1" } };
      let composerState = { dataset: { state: "idle" } };
      eval(#{(helper_source + "\nglobalThis.refreshRequired = sessionSyncRefreshRequired;").to_json});
      const externalUpdate = globalThis.refreshRequired({ mode: "external_follow", revision: "revision-2" });
      const remoteTakeover = globalThis.refreshRequired({ mode: "managed", revision: "revision-2" });
      liveOutput.dataset.sessionSyncRevision = "revision-1";
      composerState.dataset.state = "running";
      const blockedTaskSettled = globalThis.refreshRequired({ mode: "external_follow", revision: "revision-1", gateway_busy: false });
      composerState.dataset.state = "idle";
      liveOutput.dataset.sessionSyncMode = "managed";
      liveOutput.dataset.sessionSyncRevision = "revision-2";
      const retiredClient = globalThis.refreshRequired({ mode: "available", revision: "revision-2" });
      const unchangedManaged = globalThis.refreshRequired({ mode: "managed", revision: "revision-2" });
      liveOutput.dataset.sessionSyncMode = "available";
      const remoteManaged = globalThis.refreshRequired({ mode: "managed", revision: "revision-2" });
      console.log(JSON.stringify({ externalUpdate, remoteTakeover, blockedTaskSettled, retiredClient, unchangedManaged, remoteManaged }));
    JS

    assert_equal true, results.fetch("externalUpdate")
    assert_equal true, results.fetch("remoteTakeover")
    assert_equal true, results.fetch("blockedTaskSettled")
    assert_equal true, results.fetch("retiredClient")
    assert_equal false, results.fetch("unchangedManaged")
    assert_equal false, results.fetch("remoteManaged")
  end

  def test_external_refresh_restores_scrolled_up_position_and_disables_auto_follow
    app_source = File.read(File.join(ASSETS, "app.js"))
    helper_source = app_source.match(/function restorePreservedConversationScroll\(.*?(?=\nfunction initializeSessionView)/m).to_s

    results = run_javascript(<<~JS)
      let stopped = 0;
      const anchor = { dataset: { messageFingerprint: "message-2" }, getBoundingClientRect: () => ({ top: 150 }) };
      const conversationScroll = {
        scrollTop: 0, scrollHeight: 1000, clientHeight: 200,
        querySelectorAll: () => [anchor],
        getBoundingClientRect: () => ({ top: 20 })
      };
      const conversationController = { stopAutoFollow() { stopped += 1; } };
      eval(#{(helper_source + "\nglobalThis.restoreScroll = restorePreservedConversationScroll;").to_json});
      const restored = globalThis.restoreScroll({ top: 320, nearBottom: false });
      const restoredTop = conversationScroll.scrollTop;
      const anchorRestored = globalThis.restoreScroll({ top: 700, nearBottom: false, anchorFingerprint: "message-2", anchorOffset: 40 });
      const anchoredTop = conversationScroll.scrollTop;
      const nearBottomRestored = globalThis.restoreScroll({ top: 700, nearBottom: true });
      console.log(JSON.stringify({ restored, restoredTop, anchorRestored, anchoredTop, stopped, nearBottomRestored }));
    JS

    assert_equal true, results.fetch("restored")
    assert_equal 320, results.fetch("restoredTop")
    assert_equal true, results.fetch("anchorRestored")
    assert_equal 410, results.fetch("anchoredTop")
    assert_equal 2, results.fetch("stopped")
    assert_equal false, results.fetch("nearBottomRestored")
  end

  def test_page_keyboard_intent_listener_remains_page_lifetime_state
    script = File.read(File.join(ASSETS, "app.js"))

    assert_includes script, 'document.addEventListener("keydown", recordKeyboardConversationScrollIntent);'
    assert_equal 1, script.scan("bindPageLifetimeControls();").length
  end

  private

  def module_url(name)
    "file://#{File.join(ASSETS, name)}"
  end

  def run_javascript(source)
    stdout, stderr, status = Open3.capture3("node", "--input-type=module", "-e", source)
    assert status.success?, stderr
    JSON.parse(stdout)
  end
end
