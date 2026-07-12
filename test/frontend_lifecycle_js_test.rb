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

  def test_composer_focus_follows_work_lifecycle_on_desktop
    app_source = File.read(File.join(ASSETS, "app.js"))
    focus_source = app_source.match(/function automaticComposerFocusEnabled\(\).*?(?=\nfunction desktopConversationFocusEnabled)/m).to_s

    results = run_javascript(<<~JS)
      let modalOpen = false;
      const focused = [];
      const focusTarget = (name) => ({ focus(options) { focused.push([name, options]); } });
      const window = { matchMedia: () => ({ matches: true }) };
      const modalIsOpen = () => modalOpen;
      const promptTextarea = focusTarget("prompt");
      const conversationScroll = focusTarget("conversation");
      let composerState = { dataset: { state: "idle" } };
      eval(#{(focus_source + "\nglobalThis.syncComposerFocusUnderTest = syncComposerFocus;").to_json});

      globalThis.syncComposerFocusUnderTest();
      globalThis.syncComposerFocusUnderTest("sending");
      globalThis.syncComposerFocusUnderTest("running");
      globalThis.syncComposerFocusUnderTest("done");
      globalThis.syncComposerFocusUnderTest("error");
      modalOpen = true;
      globalThis.syncComposerFocusUnderTest("running");

      console.log(JSON.stringify(focused));
    JS

    assert_equal [
      ["prompt", { "preventScroll" => true }],
      ["conversation", { "preventScroll" => true }],
      ["conversation", { "preventScroll" => true }],
      ["prompt", { "preventScroll" => true }],
      ["prompt", { "preventScroll" => true }]
    ], results
  end

  def test_composer_focus_remains_automatic_only_for_fine_pointers
    app_source = File.read(File.join(ASSETS, "app.js"))
    focus_source = app_source.match(/function automaticComposerFocusEnabled\(\).*?(?=\nfunction desktopConversationFocusEnabled)/m).to_s

    results = run_javascript(<<~JS)
      let focusCount = 0;
      const window = { matchMedia: () => ({ matches: false }) };
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
      const exitSessionShortcutMode = () => {};
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
