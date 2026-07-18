require "minitest/autorun"
require "json"
require "open3"

class FrontendLifecycleJsTest < Minitest::Test
  ASSETS = File.expand_path("../public/assets", __dir__)

  def test_composer_autocomplete_follows_session_lifecycle_and_keyboard_precedence
    source = File.read(File.join(ASSETS, "app.js"))

    assert_includes source, 'composerAutocompleteController.bind(promptTextarea, document.getElementById("composer-path-list"));'
    assert_match(/function resetSessionViewState\(\) \{.*?composerAutocompleteController\.destroy\(\);/m, source)
    assert_match(/if \(promptTextarea\.value\.startsWith\("\/"\).*?selectHighlightedCommand\(\);.*?if \(composerAutocompleteController\.handleKeydown\(event\)\) return;.*?cycleThinkingShortcut.*?toggleConversationPromptFocus.*?streamingBehaviorOverride = event\.altKey/m, source)
    assert_includes source, 'if (event.key === "Escape" && !event.defaultPrevented && confirmOrStopRunningTask(event)) return;'
    assert_match(/event\.type === "compaction_end".*?composerCompacting = "false";.*?selectSteeringBehavior\(\);.*?setComposerState\("running"/m, source)
  end

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

  def test_escape_keeps_composer_stopping_despite_waiting_and_stale_running_updates
    app_source = File.read(File.join(ASSETS, "app.js"))
    composer_source = app_source.match(/function updateWaitingForOutputStatus\(\).*?(?=\nfunction resizePromptTextarea)/m).to_s
    stop_source = app_source.match(/function confirmOrStopRunningTask\(event\).*?(?=\nfunction composingQueuedMessage)/m).to_s

    results = run_javascript(<<~JS)
      let now = 10_000;
      Date.now = () => now;
      let waitingForOutputSince = 5_000;
      let waitingForOutputTimer = null;
      let waitingForOutputLabel = "Pi is running…";
      let escapeStopConfirmationExpiresAt = 0;
      const stoppingSessionPaths = new Set();
      const currentSessionPath = () => "session-a";
      const classList = { toggle() {} };
      const composerState = { dataset: { state: "running" }, textContent: "Pi is running…" };
      const abortButton = { disabled: false };
      const sendButton = { hidden: false, disabled: false, textContent: "", setAttribute() {} };
      const streamingBehaviorControls = null;
      const selectSteeringBehavior = () => {};
      const updateStreamingBehaviorControls = () => {};
      const selectedStreamingBehavior = () => "steer";
      const composerStopButton = { hidden: false, disabled: false, classList };
      const promptTextarea = { disabled: false };
      const imageInput = null;
      const attachButton = null;
      const sessionStatusBar = null;
      const liveOutput = null;
      const syncComposerFocus = () => {};
      const resetEventPollBackoff = () => {};
      const scheduleNextEventPoll = () => {};
      const sidebarController = { requestRefresh() {} };
      const updatePromptPlaceholder = () => {};
      const updateCommandListForPrompt = () => {};
      const selectedSettingsModel = () => null;
      const document = { querySelector: () => null };
      const formatWaitDuration = () => "5s";
      const ESCAPE_STOP_CONFIRMATION_WINDOW_MS = 2_000;
      const statuses = [];
      const showStatus = (message) => statuses.push(message);
      let abortRequests = 0;
      const abortForm = { requestSubmit() { abortRequests += 1; } };
      const event = { repeat: false, preventDefault() {} };
      eval(#{(composer_source + "\n" + stop_source + "\nglobalThis.confirmStop = confirmOrStopRunningTask; globalThis.updateWait = updateWaitingForOutputStatus; globalThis.setState = setComposerState;").to_json});

      globalThis.confirmStop(event);
      now += 100;
      globalThis.confirmStop(event);
      globalThis.updateWait();
      globalThis.setState("running", "Pi is running…");
      globalThis.confirmStop(event);

      console.log(JSON.stringify({
        state: composerState.dataset.state,
        label: composerState.textContent,
        promptDisabled: promptTextarea.disabled,
        stopDisabled: composerStopButton.disabled,
        abortRequests
      }));
    JS

    assert_equal "stopping", results.fetch("state")
    assert_equal "Stopping current task…", results.fetch("label")
    assert_equal true, results.fetch("promptDisabled")
    assert_equal true, results.fetch("stopDisabled")
    assert_equal 1, results.fetch("abortRequests")
  end

  def test_abort_failure_only_restores_the_same_session_while_stopping
    app_source = File.read(File.join(ASSETS, "app.js"))
    abort_source = app_source.match(/async function submitAbort\(event\).*?(?=\nfunction confirmOrStopRunningTask)/m).to_s

    results = run_javascript(<<~JS)
      let sessionPath = "session-a";
      const currentSessionPath = () => sessionPath;
      const composerState = { dataset: { state: "running" } };
      const liveOutput = null;
      const liveBusySince = 1_000;
      const transitions = [];
      const statuses = [];
      const setComposerState = (state) => {
        transitions.push(state);
        composerState.dataset.state = state;
        if (state === "stopping") stoppingSessionPaths.add(currentSessionPath());
      };
      const showStatus = (message) => statuses.push(message);
      const showSessionSwitching = () => {};
      const hideSessionSwitching = () => {};
      const scheduleNextEventPoll = () => {};
      const sidebarController = { refresh: async () => {} };
      const stoppingSessionPaths = new Set();
      let abortForm = { action: "/abort", dataset: {} };
      globalThis.FormData = class {};
      let resolveRequest;
      globalThis.fetch = () => new Promise((resolve) => { resolveRequest = resolve; });
      const event = { preventDefault() {} };
      eval(#{(abort_source + "\nglobalThis.submitAbortUnderTest = submitAbort;").to_json});

      let request = globalThis.submitAbortUnderTest(event);
      resolveRequest({ ok: false });
      await request;

      stoppingSessionPaths.add("session-a");
      composerState.dataset.state = "stopping";
      request = globalThis.submitAbortUnderTest(event);
      composerState.dataset.state = "done";
      resolveRequest({ ok: false });
      await request;

      stoppingSessionPaths.add("session-a");
      composerState.dataset.state = "stopping";
      const submittedForm = abortForm;
      request = globalThis.submitAbortUnderTest(event);
      abortForm = { action: "/abort", dataset: { submitting: "true" } };
      sessionPath = "session-b";
      resolveRequest({ ok: false });
      await request;

      console.log(JSON.stringify({
        transitions,
        statuses,
        state: composerState.dataset.state,
        markerCleared: !stoppingSessionPaths.has("session-a"),
        submittedFormCleared: submittedForm.dataset.submitting === undefined,
        replacementFormSubmitting: abortForm.dataset.submitting
      }));
    JS

    assert_equal ["stopping", "running"], results.fetch("transitions")
    assert_equal ["Stopping current task…", "Stop failed"], results.fetch("statuses")
    assert_equal "stopping", results.fetch("state")
    assert_equal true, results.fetch("markerCleared")
    assert_equal true, results.fetch("submittedFormCleared")
    assert_equal "true", results.fetch("replacementFormSubmitting")
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

  def test_main_session_history_is_tab_local_and_preserves_the_previous_selection
    app_source = File.read(File.join(ASSETS, "app.js"))
    history_source = app_source.match(/function readMainSessionHistory\(\).*?(?=\nasync function switchSession)/m).to_s

    results = run_javascript(<<~JS)
      const MAIN_SESSION_HISTORY_KEY = "gripi-main-session-history";
      const stored = new Map();
      const sessionStorage = {
        getItem(key) { return stored.get(key) || null; },
        setItem(key, value) { stored.set(key, value); }
      };
      const window = { location: { origin: "https://example.test", search: "" }, sessionStorage };
      eval(#{(history_source + "\nglobalThis.remember = rememberMainSessionSelection; globalThis.fallbackUrl = detachedSessionFallbackUrl;").to_json});

      globalThis.remember("first");
      globalThis.remember("second");
      const fallback = globalThis.fallbackUrl("second");
      globalThis.remember("second");

      console.log(JSON.stringify({ fallback, stored: JSON.parse(stored.get(MAIN_SESSION_HISTORY_KEY)) }));
    JS

    assert_equal "/?session=first&session_fallback_excluding=second", results.fetch("fallback")
    assert_equal({ "current" => "second", "previous" => "first" }, results.fetch("stored"))
  end

  def test_modal_backdrop_clicks_do_not_close_dialogs
    app_source = File.read(File.join(ASSETS, "app.js"))
    click_source = app_source.match(/document\.addEventListener\("click", \(event\) => \{\n  const opener = event\.target\.closest\("\[data-modal-open\]"\);.*?\n\}\);/m).to_s

    results = run_javascript(<<~JS)
      const closed = [];
      let prevented = false;
      const closeModal = (modal) => closed.push(modal?.name);
      const openModal = () => {};
      const openNewSessionModal = () => {};
      const openForkSessionModal = () => {};
      const openTreeSessionModal = () => {};
      const openModelSettingsModal = () => {};
      const currentSessionPath = () => "session";
      const addSessionViewFormParams = () => {};
      const showSessionSwitching = () => {};
      const hideSessionSwitching = () => {};
      const switchToBranchedSession = async () => {};
      const showStatus = () => {};
      const refreshCurrentSessionPreservingComposer = async () => {};
      const scheduleNextEventPoll = () => {};
      globalThis.fetch = () => Promise.resolve({ json: async () => ({}) });

      let handler;
      const document = { addEventListener(type, listener) { if (type === "click") handler = listener; }, querySelector: () => null };
      eval(#{click_source.to_json});

      const modal = { name: "modal" };
      const backdropEvent = {
        target: { closest: () => null, matches: (selector) => selector === "[data-modal]" },
        preventDefault() { prevented = true; }
      };
      handler(backdropEvent);
      const afterBackdrop = [...closed];

      const closer = { closest: (selector) => selector === "[data-modal]" ? modal : null };
      const closeButtonEvent = {
        target: { closest: (selector) => selector === "[data-modal-close]" ? closer : null },
        preventDefault() { prevented = true; }
      };
      handler(closeButtonEvent);

      console.log(JSON.stringify({ afterBackdrop, closed, prevented }));
    JS

    assert_empty results.fetch("afterBackdrop")
    assert_equal ["modal"], results.fetch("closed")
    assert_equal true, results.fetch("prevented")
  end

  def test_escape_cancels_extension_ui_modal_requests
    app_source = File.read(File.join(ASSETS, "app.js"))
    keydown_source = app_source.match(/document\.addEventListener\("keydown", \(event\) => \{\n  if \(event\.key === "Escape".*?\n\}\);/m).to_s

    results = run_javascript(<<~JS)
      const calls = [];
      const extensionUiModal = { querySelector: () => null };
      const activeExtensionUiRequest = { id: "request-1" };
      const modalIsOpen = () => true;
      const newSessionFormController = { closeSuggestions: () => false };
      const closeModal = (modal) => calls.push(["close", modal === extensionUiModal]);
      const cancelExtensionUiRequest = () => calls.push(["cancel"]);
      let prevented = false;
      let handler;
      const document = {
        querySelector: (selector) => selector === "[data-modal]:not([hidden])" ? extensionUiModal : null,
        addEventListener(type, listener) { if (type === "keydown") handler = listener; }
      };
      eval(#{keydown_source.to_json});

      handler({ key: "Escape", defaultPrevented: false, preventDefault() { prevented = true; } });

      console.log(JSON.stringify({ calls, prevented }));
    JS

    assert_equal [["cancel"]], results.fetch("calls")
    assert_equal true, results.fetch("prevented")
  end

  def test_extension_ui_dialogs_retry_transient_failures_and_advance_after_definitive_rejection
    app_source = File.read(File.join(ASSETS, "app.js"))
    dialog_source = app_source.match(/function extensionUiResponseBody\(.*?(?=\nfunction handleExtensionEditorText)/m).to_s

    results = run_javascript(<<~JS)
      let sessionViewGeneration = 4;
      let sessionPath = "session-a";
      const currentSessionPath = () => sessionPath;
      const addSessionViewFormParams = () => {};
      const statuses = [];
      const showStatus = (message) => statuses.push(message);
      const modalEvents = [];
      const openModal = () => modalEvents.push("open");
      const closeModal = () => modalEvents.push("close");
      const controls = [{ disabled: false }, { disabled: false }];
      const extensionUiModal = { querySelectorAll: () => controls };
      const extensionUiTitle = { textContent: "" };
      const extensionUiMessage = { textContent: "", hidden: true };
      const extensionUiError = { textContent: "", hidden: true };
      const extensionUiOptions = { children: [], replaceChildren() { this.children = []; }, append(child) { this.children.push(child); }, hidden: true };
      const extensionUiInputField = { hidden: true };
      const extensionUiInput = { value: "", placeholder: "" };
      const extensionUiEditorField = { hidden: true };
      const extensionUiEditor = { value: "" };
      const extensionUiSubmit = { hidden: false, textContent: "", dataset: {} };
      let activeExtensionUiRequest = null;
      let extensionUiRequestQueue = [];
      let extensionUiTimeoutTimer = null;
      let extensionUiDeliveryPending = false;
      const timers = [];
      globalThis.setTimeout = (callback) => { timers.push(callback); return timers.length; };
      globalThis.clearTimeout = () => {};
      const document = { createElement: () => ({ dataset: {}, setAttribute() {}, addEventListener() {} }) };
      const responses = [];
      globalThis.fetch = () => new Promise((resolve) => responses.push(resolve));
      eval(#{(dialog_source + "\nglobalThis.enqueue = enqueueExtensionUiDialog; globalThis.send = sendExtensionUiResponse; globalThis.active = () => activeExtensionUiRequest; globalThis.error = () => extensionUiError;").to_json});

      globalThis.enqueue({ type: "extension_ui_request", id: "one", method: "confirm" });
      globalThis.enqueue({ type: "extension_ui_request", id: "two", method: "input" });
      const firstDelivery = globalThis.send({ confirmed: "true" });
      const duplicateDelivery = await globalThis.send({ confirmed: "true" });
      responses.shift()({ ok: false, status: 500 });
      const firstResult = await firstDelivery;
      const afterFailure = { id: globalThis.active()?.id, error: globalThis.error().textContent, disabled: controls[0].disabled };

      const retry = globalThis.send({ confirmed: "true" });
      responses.shift()({ ok: false, status: 422 });
      const retryResult = await retry;
      const afterRejection = globalThis.active()?.id;
      const nextDelivery = globalThis.send({ value: "answer" });
      responses.shift()({ ok: true, status: 200 });
      const nextResult = await nextDelivery;
      globalThis.enqueue({ type: "extension_ui_request", id: "three", method: "confirm" });
      sessionPath = "session-b";
      sessionViewGeneration += 1;
      const staleResult = await globalThis.send({ value: "wrong session" });

      console.log(JSON.stringify({
        duplicateDelivery, firstResult, retryResult, nextResult, staleResult, fetchCount: responses.length,
        afterFailure, afterRejection, activeId: globalThis.active()?.id || null, modalEvents, statuses
      }));
    JS

    assert_equal false, results.fetch("duplicateDelivery")
    assert_equal false, results.fetch("firstResult")
    assert_equal false, results.fetch("retryResult")
    assert_equal "two", results.fetch("afterRejection")
    assert_equal true, results.fetch("nextResult")
    assert_equal false, results.fetch("staleResult")
    assert_equal({ "id" => "one", "error" => "Could not answer extension request. Please try again.", "disabled" => false }, results.fetch("afterFailure"))
    assert_equal "three", results.fetch("activeId")
    assert_equal ["open", "close", "open", "close", "open"], results.fetch("modalEvents")
    assert_equal ["Could not answer extension request"], results.fetch("statuses")
  end

  def test_extension_ui_dialog_timeout_advances_queue_without_sending_a_response
    app_source = File.read(File.join(ASSETS, "app.js"))
    dialog_source = app_source.match(/function extensionUiResponseBody\(.*?(?=\nfunction handleExtensionEditorText)/m).to_s

    results = run_javascript(<<~JS)
      let now = 1_000;
      Date.now = () => now;
      let sessionViewGeneration = 1;
      const currentSessionPath = () => "session-a";
      const addSessionViewFormParams = () => {};
      const showStatus = () => {};
      const openModal = () => {};
      const closeModal = () => {};
      const extensionUiModal = { querySelectorAll: () => [] };
      const extensionUiTitle = { textContent: "" };
      const extensionUiMessage = { textContent: "", hidden: true };
      const extensionUiError = { textContent: "", hidden: true };
      const extensionUiOptions = { replaceChildren() {}, append() {}, hidden: true };
      const extensionUiInputField = { hidden: true };
      const extensionUiInput = { value: "", placeholder: "" };
      const extensionUiEditorField = { hidden: true };
      const extensionUiEditor = { value: "" };
      const extensionUiSubmit = { hidden: false, textContent: "", dataset: {} };
      let activeExtensionUiRequest = null;
      let extensionUiRequestQueue = [];
      let extensionUiTimeoutTimer = null;
      let extensionUiDeliveryPending = false;
      let timeoutCallback;
      globalThis.setTimeout = (callback) => { timeoutCallback = callback; return 1; };
      globalThis.clearTimeout = () => {};
      const document = { createElement: () => ({ dataset: {}, setAttribute() {}, addEventListener() {} }) };
      let fetchCount = 0;
      globalThis.fetch = async () => { fetchCount += 1; return { ok: true }; };
      eval(#{(dialog_source + "\nglobalThis.enqueue = enqueueExtensionUiDialog; globalThis.active = () => activeExtensionUiRequest;").to_json});

      globalThis.enqueue({ id: "timed", method: "confirm", timeout: 100 });
      globalThis.enqueue({ id: "next", method: "confirm" });
      now = 1_100;
      timeoutCallback();

      console.log(JSON.stringify({ activeId: globalThis.active()?.id, fetchCount }));
    JS

    assert_equal "next", results.fetch("activeId")
    assert_equal 0, results.fetch("fetchCount")
  end

  def test_detaching_switches_the_main_view_while_leaving_popup_navigation_to_the_native_link
    app_source = File.read(File.join(ASSETS, "app.js"))
    detach_source = app_source.match(/function detachSession\(.*?\n\}/m).to_s
    click_source = app_source.match(/document\.addEventListener\("click", \(event\) => \{\n  const link = event\.target\.closest\("\.session-header-window-action"\);.*?\n\}\);/m).to_s

    results = run_javascript(<<~JS)
      const calls = [];
      const currentSessionPath = () => "detached";
      const detachedSessionFallbackUrl = () => "/?session=previous&session_fallback_excluding=detached";
      const switchSession = async (...args) => { calls.push(args); return true; };
      eval(#{(detach_source + "\nglobalThis.detachSessionUnderTest = detachSession;").to_json});

      const result = await globalThis.detachSessionUnderTest();
      console.log(JSON.stringify({ calls, result }));
    JS

    assert_equal [["/?session=previous&session_fallback_excluding=detached", { "push" => true, "focus" => true }]], results.fetch("calls")
    assert_equal true, results.fetch("result")
    refute_includes click_source, "preventDefault"
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
      const rememberMainSessionSelection = () => {};
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
