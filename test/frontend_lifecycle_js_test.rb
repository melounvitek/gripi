require "minitest/autorun"
require "json"
require "open3"

class FrontendLifecycleJsTest < Minitest::Test
  ASSETS = File.expand_path("../public/assets", __dir__)

  def test_composer_autocomplete_follows_session_lifecycle_and_keyboard_precedence
    source = File.read(File.join(ASSETS, "app.js"))

    assert_includes source, 'composerAutocompleteController.bind(promptTextarea, document.getElementById("composer-path-list"));'
    assert_match(/function bindSessionDom\(\) \{.*?projectSelectController\.initialize\(conversationPanel\);.*?conversationController\.bind\(promptTextarea\);/m, source)
    assert_match(/function resetSessionViewState\(\) \{.*?projectSelectController\.destroy\(conversationPanel\);.*?conversationController\.reset\(\);/m, source)
    assert_match(/if \(promptTextarea\.value\.startsWith\("\/"\).*?selectHighlightedCommand\(\);.*?if \(composerAutocompleteController\.handleKeydown\(event\)\) return;.*?cycleThinkingShortcut.*?toggleConversationPromptFocus.*?keyboardStreamingBehaviorOverride = event\.altKey/m, source)
    assert_includes source, 'if (event.key === "Escape" && !event.defaultPrevented && confirmOrStopRunningTask(event)) return;'
    assert_match(/event\.type === "compaction_end".*?composerCompacting = "false";.*?setComposerState\("running"/m, source)
    assert_match(/let touchSendMenuPointerDown = false;.*?addEventListener\("click", \(event\).*?event\.detail === 0.*?\.focus\(\);.*?addEventListener\("focusout".*?if \(touchSendMenuPointerDown\) return;.*?addEventListener\("keydown".*?touchSendMenuPointerDown = false;.*?addEventListener\("pointerdown".*?event\.pointerType === "touch".*?addEventListener\("pointercancel"/m, source)
  end

  def test_streaming_send_control_selects_follow_up_without_submitting_and_can_reset_to_steer
    app_source = File.read(File.join(ASSETS, "app.js"))
    helper_source = app_source.match(/function selectedStreamingBehavior\(\).*?(?=\nfunction updatePromptPlaceholder)/m).to_s

    results = run_javascript(<<~JS)
      let keyboardStreamingBehaviorOverride = null;
      let streamingBehaviorSelection = "steer";
      let composerState = { dataset: { state: "running" } };
      let liveOutput = { dataset: { composerCompacting: "false" } };
      let promptFocuses = 0;
      let sendFocuses = 0;
      let placeholderBehavior = null;
      const menuItem = {};
      const document = { activeElement: null };
      const promptTextarea = { focus() { promptFocuses += 1; } };
      const sendButton = { textContent: "", setAttribute() {}, focus() { sendFocuses += 1; } };
      const sendControl = { classList: { toggle() {} } };
      const sendMenuToggle = { hidden: false, setAttribute() {}, focus() {} };
      const behaviorButtons = ["steer", "follow_up"].map((behavior) => ({ dataset: { streamingBehavior: behavior }, setAttribute(name, value) { this[name] = value; } }));
      const sendMenu = {
        hidden: false,
        contains(element) { return element === menuItem; },
        querySelectorAll() { return behaviorButtons; }
      };
      const updatePromptPlaceholder = () => { placeholderBehavior = streamingBehaviorSelection; };
      eval(#{(helper_source + "\nglobalThis.selected = selectedStreamingBehavior; globalThis.submitted = submittedStreamingBehavior; globalThis.selectBehavior = selectStreamingBehavior; globalThis.closeMenu = closeSendMenu; globalThis.updateControl = updateStreamingSendControl;").to_json});

      const defaultBehavior = globalThis.selected();
      globalThis.selectBehavior("follow_up");
      const selectedFollowUp = globalThis.selected();
      const submittedFollowUp = globalThis.submitted();
      const followUpLabel = sendButton.textContent;
      const followUpPlaceholder = placeholderBehavior;
      const selectedStates = behaviorButtons.map((button) => button["aria-pressed"]);
      globalThis.selectBehavior("steer", { focus: false });
      const resetBehavior = globalThis.selected();
      document.activeElement = menuItem;
      globalThis.closeMenu();
      document.activeElement = sendMenuToggle;
      liveOutput.dataset.composerCompacting = "true";
      globalThis.updateControl();
      const compactingBehavior = globalThis.selected();
      composerState.dataset.state = "idle";
      const idleBehavior = globalThis.submitted();

      console.log(JSON.stringify({ defaultBehavior, selectedFollowUp, submittedFollowUp, followUpLabel, followUpPlaceholder, selectedStates, resetBehavior, compactingBehavior, idleBehavior, menuHidden: sendMenu.hidden, promptFocuses, sendFocuses }));
    JS

    assert_equal "steer", results.fetch("defaultBehavior")
    assert_equal "follow_up", results.fetch("selectedFollowUp")
    assert_equal "follow_up", results.fetch("submittedFollowUp")
    assert_equal "Queue", results.fetch("followUpLabel")
    assert_equal ["false", "true"], results.fetch("selectedStates")
    assert_equal "follow_up", results.fetch("followUpPlaceholder")
    assert_equal "steer", results.fetch("resetBehavior")
    assert_equal "follow_up", results.fetch("compactingBehavior")
    assert_nil results.fetch("idleBehavior")
    assert_equal true, results.fetch("menuHidden")
    assert_equal 1, results.fetch("promptFocuses")
    assert_equal 2, results.fetch("sendFocuses")
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

  def test_live_session_name_update_does_not_add_a_title_around_the_view_selector
    app_source = File.read(File.join(ASSETS, "app.js"))
    helper_source = app_source.match(/function updateSessionHeaderName\(name\) \{.*?\n\}/m).to_s
      .sub("function updateSessionHeaderName(name)", "globalThis.updateSessionHeaderName = function(name)")

    results = run_javascript(<<~JS)
      const titleContainer = { title: "", querySelector() { return { textContent: "project" }; } };
      const headerName = { textContent: "Old name", title: "Old name", closest() { return titleContainer; } };
      const document = { title: "", querySelector: () => headerName };
      let baseDocumentTitle = "Old name · Gripi";
      let renders = 0;
      const renderDocumentTitle = () => { renders += 1; };
      eval(#{helper_source.to_json});

      updateSessionHeaderName("New name");
      console.log(JSON.stringify({
        text: headerName.textContent,
        headingTitle: headerName.title,
        containerTitle: titleContainer.title,
        baseDocumentTitle,
        renders
      }));
    JS

    assert_equal "New name", results.fetch("text")
    assert_equal "New name", results.fetch("headingTitle")
    assert_equal "", results.fetch("containerTitle")
    assert_equal "New name · Gripi", results.fetch("baseDocumentTitle")
    assert_equal 1, results.fetch("renders")
  end

  def test_lazy_history_waits_for_terminal_hydration_before_preserving_the_viewport
    results = run_javascript(<<~JS)
      const { ConversationController } = await import(#{module_url("conversation_controller.js").to_json});
      const content = { hydrated: false, querySelectorAll: () => [] };
      const template = { content, set innerHTML(_value) {} };
      const document = { createElement: () => template, body: { classList: { remove() {} } } };
      const controller = new ConversationController(document, {});
      let inserted = false;
      let hydratedAtInsert = false;
      const element = {
        scrollTop: 10,
        scrollHeight: 100,
        insertBefore(fragment) { inserted = true; hydratedAtInsert = fragment.hydrated; this.scrollHeight = 160; }
      };
      controller.element = element;
      controller.bindingEpoch = 1;
      controller.refreshFocusedActivity = () => {};
      controller.updateJumpControls = () => {};
      controller.historyEnhancer = async (root) => {
        await Promise.resolve();
        root.hydrated = true;
      };

      const insertion = controller.insertHistoryHtml("<article></article>", null, true);
      const insertedSynchronously = inserted;
      await insertion;
      console.log(JSON.stringify({ insertedSynchronously, inserted, hydratedAtInsert, scrollTop: element.scrollTop }));
    JS

    assert_equal false, results.fetch("insertedSynchronously")
    assert_equal true, results.fetch("inserted")
    assert_equal true, results.fetch("hydratedAtInsert")
    assert_equal 70, results.fetch("scrollTop")
  end

  def test_cancelled_lazy_history_does_not_insert_after_terminal_hydration
    results = run_javascript(<<~JS)
      const { ConversationController } = await import(#{module_url("conversation_controller.js").to_json});
      const content = { querySelectorAll: () => [] };
      const template = { content, set innerHTML(_value) {} };
      const document = { createElement: () => template, body: { classList: { remove() {} } } };
      const controller = new ConversationController(document, {});
      let inserted = false;
      let finishHydration;
      controller.element = { scrollTop: 0, scrollHeight: 100, insertBefore() { inserted = true; } };
      controller.bindingEpoch = 1;
      controller.historyRequestGeneration = 3;
      controller.refreshFocusedActivity = () => {};
      controller.updateJumpControls = () => {};
      controller.historyEnhancer = () => new Promise((resolve) => { finishHydration = resolve; });

      const insertion = controller.insertHistoryHtml("<article></article>", null, true);
      controller.historyRequestGeneration += 1;
      finishHydration();
      await insertion;
      console.log(JSON.stringify({ inserted }));
    JS

    assert_equal false, results.fetch("inserted")
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
    focus_source = app_source.match(/function automaticComposerFocusEnabled\(\).*?(?=\nfunction toggleConversationPromptFocus)/m).to_s

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
      let escapeStopConfirmationTimer = null;
      const stoppingSessionPaths = new Set();
      const currentSessionPath = () => "session-a";
      const classList = { toggle() {} };
      const composerState = { dataset: { state: "running" }, textContent: "Pi is running…" };
      let streamingBehaviorSelection = "steer";
      const abortButton = { disabled: false };
      const sendButton = { hidden: false, disabled: false, textContent: "", setAttribute() {} };
      const updateStreamingSendControl = () => {};
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

  def test_completed_bash_does_not_regress_when_its_start_event_arrives_late
    app_source = File.read(File.join(ASSETS, "app.js"))
    lifecycle_source = app_source.match(/function startLiveBash\(.*?(?=\nfunction renderEvent)/m).to_s

    results = run_javascript(<<~JS)
      let liveBash = { id: "bash-1" };
      const completedBashIds = new Set();
      const rendered = [];
      const liveMessageRenderer = {
        bashExecutionCompleted: (bashId) => completedBashIds.has(bashId),
        renderBashEvent: (event) => {
          rendered.push(event.type);
          if (event.type !== "bash_start") completedBashIds.add(event.bashId);
        }
      };
      const composerState = { dataset: { state: "bash" } };
      const liveOutput = { dataset: { composerCompacting: "false" } };
      const stoppingSessionPaths = new Set();
      const currentSessionPath = () => "session-a";
      let liveAgentRunning = false;
      const eventTimeMilliseconds = (event) => event.gatewayTimestamp;
      const setComposerState = () => {};
      const showCurrentActiveTask = () => {};
      const resetEventPollBackoff = () => {};
      eval(#{(lifecycle_source + "\nglobalThis.startBash = startLiveBash; globalThis.finishBash = finishLiveBash;").to_json});

      globalThis.finishBash({ type: "bash_end", bashId: "bash-1", result: { output: "done" }, gatewayTimestamp: 2_000 });
      globalThis.finishBash({ type: "bash_end", bashId: "bash-1", result: { output: "done" }, gatewayTimestamp: 3_000 });
      globalThis.startBash({ type: "bash_start", bashId: "bash-1", command: "ignored late start", gatewayTimestamp: 4_000 });
      console.log(JSON.stringify({ active: liveBash, rendered, completed: completedBashIds.has("bash-1") }));
    JS

    assert_nil results.fetch("active")
    assert_equal ["bash_end"], results.fetch("rendered")
    assert results.fetch("completed")
  end

  def test_agent_ui_wins_over_bash_and_settling_returns_to_the_remaining_task
    app_source = File.read(File.join(ASSETS, "app.js"))
    state_source = app_source.match(/function showCurrentActiveTask\(.*?(?=\nfunction setComposerState)/m).to_s

    results = run_javascript(<<~JS)
      let liveAgentRunning = false;
      let liveBash = { id: "bash-1" };
      let liveBusySince = 1_000;
      const liveOutput = { dataset: { composerCompacting: "false" } };
      const transitions = [];
      const setComposerState = (state, label) => transitions.push([state, label]);
      eval(#{(state_source + "\nglobalThis.showTask = showCurrentActiveTask;").to_json});

      globalThis.showTask();
      liveAgentRunning = true;
      globalThis.showTask();
      liveAgentRunning = false;
      globalThis.showTask();
      liveOutput.dataset.composerCompacting = "true";
      globalThis.showTask();
      liveOutput.dataset.composerCompacting = "false";
      liveBash = null;
      globalThis.showTask();
      console.log(JSON.stringify(transitions));
    JS

    assert_equal [
      ["bash", "Shell command running…"],
      ["running", "Pi is running…"],
      ["bash", "Shell command running…"],
      ["running", "Compacting…"],
      ["done", "Done"]
    ], results
  end

  def test_bash_completion_preserves_overlapping_agent_running_state
    app_source = File.read(File.join(ASSETS, "app.js"))
    lifecycle_source = app_source.match(/function startLiveBash\(.*?(?=\nfunction renderEvent)/m).to_s

    results = run_javascript(<<~JS)
      let liveBash = { id: "bash-1" };
      let liveAgentRunning = true;
      const completedBashIds = new Set();
      const liveMessageRenderer = {
        bashExecutionCompleted: (bashId) => completedBashIds.has(bashId),
        renderBashEvent(event) { if (event.type !== "bash_start") completedBashIds.add(event.bashId); }
      };
      const composerState = { dataset: { state: "stopping" } };
      const stoppingSessionPaths = new Set(["session-a"]);
      const currentSessionPath = () => "session-a";
      const transitions = [];
      const setComposerState = (state) => {
        if (state === "running" && stoppingSessionPaths.has(currentSessionPath())) return;
        composerState.dataset.state = state;
        transitions.push(state);
      };
      const showCurrentActiveTask = () => setComposerState(liveAgentRunning ? "running" : "idle");
      const eventTimeMilliseconds = () => 2_000;
      const resetEventPollBackoff = () => {};
      eval(#{(lifecycle_source + "\nglobalThis.finishBash = finishLiveBash;").to_json});

      globalThis.finishBash({ type: "bash_end", bashId: "bash-1", result: { output: "done" } });
      console.log(JSON.stringify({ liveBash, transitions, state: composerState.dataset.state, stopping: stoppingSessionPaths.has("session-a") }));
    JS

    assert_nil results.fetch("liveBash")
    assert_equal ["running"], results.fetch("transitions")
    assert_equal "running", results.fetch("state")
    refute results.fetch("stopping")
  end

  def test_escape_confirmation_is_visible_for_a_bash_only_task
    app_source = File.read(File.join(ASSETS, "app.js"))
    wait_source = app_source.match(/function updateWaitingForOutputStatus\(\).*?(?=\nfunction startWaitingForOutput)/m).to_s
    stop_source = app_source.match(/function confirmOrStopRunningTask\(event\).*?(?=\nfunction composingQueuedMessage)/m).to_s

    results = run_javascript(<<~JS)
      let now = 10_000;
      Date.now = () => now;
      let waitingForOutputSince = null;
      let escapeStopConfirmationExpiresAt = 0;
      let escapeStopConfirmationTimer = null;
      let timerCallback = null;
      globalThis.setTimeout = (callback) => { timerCallback = callback; return 1; };
      globalThis.clearTimeout = () => {};
      const composerState = { dataset: { state: "bash" }, textContent: "Shell command running…" };
      const ESCAPE_STOP_CONFIRMATION_WINDOW_MS = 2_000;
      const showStatus = () => {};
      const setComposerState = () => {};
      const abortForm = { requestSubmit() {} };
      const event = { repeat: false, preventDefault() {} };
      eval(#{(wait_source + "\n" + stop_source + "\nglobalThis.confirmBashStop = confirmOrStopRunningTask;").to_json});
      const handled = globalThis.confirmBashStop(event);
      const confirmationLabel = composerState.textContent;
      now = 12_001;
      timerCallback();
      console.log(JSON.stringify({ handled, confirmationLabel, expiredLabel: composerState.textContent }));
    JS

    assert_equal({
      "handled" => true,
      "confirmationLabel" => "Press ESC again to stop current task",
      "expiredLabel" => "Shell command running…"
    }, results)
  end

  def test_bash_submission_stays_nonblocking_and_restores_a_rejected_duplicate
    app_source = File.read(File.join(ASSETS, "app.js"))
    bash_source = app_source.match(/function restoreSubmittedComposerInput\(.*?(?=\nasync function submitPrompt)/m).to_s

    results = run_javascript(<<~JS)
      let sessionViewGeneration = 1;
      let sessionSwitchGeneration = 1;
      const promptSessionInput = { value: "session-a" };
      const promptTextarea = { value: "!sleep 30", disabled: false };
      const promptForm = { action: "/prompt" };
      const commandList = { classList: { remove() {} }, removeAttribute() {} };
      const submittedFile = { name: "screen.png", type: "image/png" };
      let pendingImages = [{ file: submittedFile, url: "blob:old" }];
      let resolveRequest;
      const statuses = [];
      const polls = [];
      const currentSessionPath = () => promptSessionInput.value;
      const addSessionViewFormParams = () => {};
      const clearStoredComposerDraft = () => {};
      const persistStoredComposerDraft = () => {};
      const clearAttachments = () => { pendingImages = []; };
      const renderAttachments = () => {};
      const resetCommandSelection = () => {};
      const resizePromptTextarea = () => {};
      const resetEventPollBackoff = () => {};
      const scheduleNextEventPoll = (delay) => polls.push(delay);
      const showStatus = (message) => statuses.push(message);
      const sidebarController = { requestRefresh() {} };
      const finished = [];
      const finishLiveBash = (event) => finished.push(event);
      globalThis.URL = { createObjectURL: (file) => `blob:restored-${file.name}` };
      globalThis.FormData = class {
        set() {}
        append() {}
      };
      globalThis.fetch = () => new Promise((resolve) => { resolveRequest = resolve; });
      eval(#{(bash_source + "\nglobalThis.submitBashUnderTest = submitBashPrompt;").to_json});

      const submission = globalThis.submitBashUnderTest("!sleep 30", { command: "sleep 30", excludeFromContext: false });
      const whilePending = { value: promptTextarea.value, disabled: promptTextarea.disabled, attachmentCount: pendingImages.length };
      promptTextarea.value = "ordinary prompt";
      resolveRequest({ ok: false, json: async () => ({ error: "A bash command is already running" }) });
      await submission;
      const restoredValue = promptTextarea.value;
      const restoredFiles = pendingImages.map((entry) => entry.file.name);

      promptTextarea.value = "!bad";
      pendingImages = [];
      const acceptedFailure = globalThis.submitBashUnderTest("!bad", { command: "bad", excludeFromContext: false });
      resolveRequest({ ok: true, json: async () => ({ bash_id: "bash-2", error: "Shell unavailable" }) });
      await acceptedFailure;

      console.log(JSON.stringify({
        whilePending,
        restoredValue,
        restoredFiles,
        acceptedFailure: { value: promptTextarea.value, event: finished[0] },
        statuses,
        polls
      }));
    JS

    assert_equal({ "value" => "", "disabled" => false, "attachmentCount" => 0 }, results.fetch("whilePending"))
    assert_equal "!sleep 30\nordinary prompt", results.fetch("restoredValue")
    assert_equal ["screen.png"], results.fetch("restoredFiles")
    assert_equal "", results.dig("acceptedFailure", "value")
    assert_equal "bash_error", results.dig("acceptedFailure", "event", "type")
    assert_equal "bash-2", results.dig("acceptedFailure", "event", "bashId")
    assert_equal ["A bash command is already running"], results.fetch("statuses")
    assert_equal [0, 0, 0, 0], results.fetch("polls")
  end

  def test_prompt_and_bash_failure_restoration_merge_with_the_current_draft
    app_source = File.read(File.join(ASSETS, "app.js"))
    restore_source = app_source.match(/function restoreSubmittedComposerInput\(.*?(?=\nasync function submitBashPrompt)/m).to_s

    results = run_javascript(<<~JS)
      const currentFile = { name: "current.png" };
      const promptFile = { name: "prompt.png" };
      const bashFile = { name: "bash.png" };
      const promptTextarea = { value: "new draft" };
      let pendingImages = [{ file: currentFile, url: "blob:current" }];
      const persistStoredComposerDraft = () => {};
      const renderAttachments = () => {};
      const resizePromptTextarea = () => {};
      globalThis.URL = { createObjectURL: (file) => `blob:${file.name}` };
      eval(#{(restore_source + "\nglobalThis.restoreInput = restoreSubmittedComposerInput;").to_json});

      globalThis.restoreInput("ordinary prompt", [promptFile]);
      globalThis.restoreInput("!failed", [bashFile]);
      const promptThenBash = { text: promptTextarea.value, files: pendingImages.map((entry) => entry.file.name) };

      promptTextarea.value = "new draft";
      pendingImages = [{ file: currentFile, url: "blob:current" }];
      globalThis.restoreInput("!failed", [bashFile]);
      globalThis.restoreInput("ordinary prompt", [promptFile]);
      console.log(JSON.stringify({
        promptThenBash,
        bashThenPrompt: { text: promptTextarea.value, files: pendingImages.map((entry) => entry.file.name) }
      }));
    JS

    assert_equal "!failed\nordinary prompt\nnew draft", results.dig("promptThenBash", "text")
    assert_equal ["current.png", "prompt.png", "bash.png"], results.dig("promptThenBash", "files")
    assert_equal "ordinary prompt\n!failed\nnew draft", results.dig("bashThenPrompt", "text")
    assert_equal ["current.png", "bash.png", "prompt.png"], results.dig("bashThenPrompt", "files")
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
      const showCurrentActiveTask = () => setComposerState("running");
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
    focus_source = app_source.match(/function automaticComposerFocusEnabled\(\).*?(?=\nfunction toggleConversationPromptFocus)/m).to_s

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
    focus_source = app_source.match(/function automaticComposerFocusEnabled\(\).*?(?=\nfunction toggleConversationPromptFocus)/m).to_s

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

  def test_focused_view_preserves_conversation_content_and_compaction
    results = run_javascript(<<~JS)
      const { ConversationController } = await import(#{module_url("conversation_controller.js").to_json});
      const message = (role, classes = [], final = false) => ({
        dataset: { role, ...(final ? { finalAssistantResponse: "true" } : {}) },
        classList: { contains: (name) => classes.includes(name) }
      });
      const controller = new ConversationController({}, {});
      const cases = {
        user: message("user", ["message--user"]),
        commentary: message("assistant", ["message--assistant"]),
        finalAssistant: message("assistant", ["message--assistant"], true),
        compaction: message("status", ["message--status", "message--compact", "message--compaction"]),
        custom: message("custom", ["message--status"]),
        unknown: message("future-visible-event", ["message--status"]),
        reasoning: message("assistant", ["message--assistant", "message--thinking"]),
        tool: message("assistant", ["message--assistant", "message--tool-call"]),
        error: message("error", ["message--error"]),
        status: message("status", ["message--status"]),
        system: message("system", ["message--status"])
      };
      console.log(JSON.stringify(Object.fromEntries(Object.entries(cases).map(([name, value]) => [name, controller.focusedViewMessage(value)]))));
    JS

    assert_equal true, results.fetch("user")
    assert_equal true, results.fetch("commentary")
    assert_equal true, results.fetch("finalAssistant")
    assert_equal true, results.fetch("compaction")
    assert_equal true, results.fetch("custom")
    assert_equal true, results.fetch("unknown")
    assert_equal false, results.fetch("reasoning")
    assert_equal false, results.fetch("tool")
    assert_equal false, results.fetch("error")
    assert_equal false, results.fetch("status")
    assert_equal false, results.fetch("system")
  end

  def test_focused_activity_groups_hidden_turn_items_and_summarizes_errors
    results = run_javascript(<<~JS)
      const { ConversationController } = await import(#{module_url("conversation_controller.js").to_json});
      const message = (role, classes = [], final = false) => ({
        dataset: { role, ...(final ? { finalAssistantResponse: "true" } : {}) },
        classList: { contains: (name) => classes.includes(name) }
      });
      const controller = new ConversationController({}, {});
      const thinking = message("assistant", ["message--thinking"]);
      const tool = message("assistant", ["message--tool-call"]);
      const error = message("error", ["message--error"]);
      const status = message("status", ["message--status"]);
      const groups = controller.focusedActivityGroups([
        message("user", ["message--user"]),
        thinking,
        tool,
        error,
        message("assistant", ["message--assistant"]),
        status,
        message("status", ["message--status", "message--compaction"]),
        message("user", ["message--user"]),
        message("assistant", ["message--assistant"], true)
      ]);
      const summary = controller.focusedActivitySummary(groups[0]);
      console.log(JSON.stringify({
        groupSizes: groups.map((group) => group.length),
        firstGroupPreserved: groups[0][0] === thinking && groups[0][1] === tool && groups[0][2] === error,
        summary
      }));
    JS

    assert_equal [3, 1], results.fetch("groupSizes")
    assert_equal true, results.fetch("firstGroupPreserved")
    assert_equal "1 reasoning step · 1 tool update", results.dig("summary", "text")
    assert_equal 1, results.dig("summary", "errorCount")
  end

  def test_focused_activity_does_not_group_messages_across_an_unloaded_gap
    results = run_javascript(<<~JS)
      const { ConversationController } = await import(#{module_url("conversation_controller.js").to_json});
      const message = () => ({
        dataset: { role: "assistant" },
        classList: { contains: (name) => name === "message--thinking" }
      });
      const first = message(); const second = message(); const gap = { hidden: false };
      first.nextElementSibling = gap; gap.nextElementSibling = second;
      const controller = new ConversationController({}, {});
      controller.historyStatus = () => gap;
      const open = controller.focusedActivityGroups([first, second]).map((group) => group.length);
      gap.hidden = true;
      const closed = controller.focusedActivityGroups([first, second]).map((group) => group.length);
      console.log(JSON.stringify({ open, closed }));
    JS

    assert_equal({ "open" => [1, 1], "closed" => [2] }, results)
  end

  def test_focused_activity_lists_only_tool_calls_and_errors
    results = run_javascript(<<~JS)
      const { ConversationController } = await import(#{module_url("conversation_controller.js").to_json});
      const message = (classes, summary = "", body = "", toolCallId = "") => ({
        dataset: { toolCallId },
        classList: { contains: (name) => classes.includes(name) },
        querySelector(selector) {
          if (selector === ".compact-summary" && summary) return { textContent: summary };
          if (selector === ".message-body" && body) return { textContent: body };
          return null;
        }
      });
      const controller = new ConversationController({}, {});
      const items = controller.focusedActivityItems([
        message(["message--thinking"], "", "Reasoning text"),
        message(["message--tool-call"], "read   public/assets/app.js"),
        message(["message--tool"], "tool result", "large result"),
        message(["message--error"], "", "Request timed out"),
        message(["message--tool-call"], "bash   mise run test", "", "call-1"),
        message(["message--tool-error"], "", "command failed", "call-1")
      ]);
      console.log(JSON.stringify(items));
    JS

    assert_equal [
      { "type" => "tool", "text" => "read public/assets/app.js" },
      { "type" => "error", "text" => "Request timed out" },
      { "type" => "error", "text" => "bash mise run test" }
    ], results
  end

  def test_focused_activity_renders_only_the_ten_most_recent_items
    results = run_javascript(<<~JS)
      const { ConversationController } = await import(#{module_url("conversation_controller.js").to_json});
      const descendants = (node) => node.children.flatMap((child) => [child, ...descendants(child)]);
      const render = (itemCount) => {
        const summaries = [];
        const messages = Array.from({ length: itemCount }, (_, index) => ({
          dataset: {},
          classList: { contains: (name) => name === "message--tool-call" },
          querySelector(selector) { return selector === ".compact-summary" ? { textContent: `tool ${index + 1}` } : null; },
          before(summary) { summaries.push(summary); }
        }));
        const element = {
          querySelectorAll(selector) {
            if (selector === ".message") return messages;
            if (selector === "[data-focus-activity-summary]") return summaries;
            return [];
          },
          querySelector: () => null
        };
        const document = {
          createElement(tagName) {
            return {
              tagName: tagName.toUpperCase(), dataset: {}, children: [], className: "", attributes: {},
              append(...children) { this.children.push(...children); },
              setAttribute(name, value) { this.attributes[name] = value; },
              remove() {}
            };
          }
        };
        const controller = new ConversationController(document, {});
        controller.element = element;
        controller.refreshFocusedActivity();
        const children = descendants(summaries[0]);
        return {
          hiddenNoticeTag: children.find((child) => child.className === "focus-activity-hidden-count")?.tagName,
          hiddenNotice: children.find((child) => child.className === "focus-activity-hidden-count")?.textContent,
          items: children
            .filter((child) => child.className.startsWith("focus-activity-item "))
            .map((child) => descendants(child).find((item) => item.className === "focus-activity-item-text")?.textContent)
        };
      };
      console.log(JSON.stringify({ singular: render(11), plural: render(12) }));
    JS

    assert_equal "P", results.dig("singular", "hiddenNoticeTag")
    assert_equal "… (1 previous item hidden)", results.dig("singular", "hiddenNotice")
    assert_equal (2..11).map { |index| "tool #{index}" }, results.dig("singular", "items")
    assert_equal "… (2 previous items hidden)", results.dig("plural", "hiddenNotice")
    assert_equal (3..12).map { |index| "tool #{index}" }, results.dig("plural", "items")
  end

  def test_focused_activity_running_state_refreshes_only_when_it_changes
    results = run_javascript(<<~JS)
      const { ConversationController } = await import(#{module_url("conversation_controller.js").to_json});
      const controller = new ConversationController({}, {});
      let refreshes = 0;
      controller.scheduleFocusedActivityRefresh = () => { refreshes += 1; };
      controller.setAgentRunning(true);
      controller.setAgentRunning(true);
      const running = controller.agentRunning;
      controller.setAgentRunning(false);
      console.log(JSON.stringify({ running, settled: controller.agentRunning, refreshes }));
    JS

    assert_equal true, results.fetch("running")
    assert_equal false, results.fetch("settled")
    assert_equal 2, results.fetch("refreshes")
  end

  def test_focused_activity_renders_static_timelines_with_running_state_on_latest_group
    results = run_javascript(<<~JS)
      const { ConversationController } = await import(#{module_url("conversation_controller.js").to_json});
      const classes = (...names) => ({ contains: (name) => names.includes(name) });
      const summaries = [];
      const message = (role, names, compactSummary = "") => ({
        dataset: { role }, classList: classes(...names),
        querySelector(selector) { return selector === ".compact-summary" && compactSummary ? { textContent: compactSummary } : null; },
        before(summary) { summaries.push(summary); }
      });
      const thinking = message("assistant", ["message--thinking"]);
      const user = message("user", ["message--user"]);
      const tool = message("assistant", ["message--tool-call"], "bash   mise run test");
      const messages = [thinking, user, tool];
      const element = {
        querySelectorAll(selector) {
          if (selector === ".message") return messages;
          if (selector === "[data-focus-activity-summary]") return summaries;
          return [];
        }
      };
      const document = {
        createElement(tagName) {
          return {
            tagName: tagName.toUpperCase(), dataset: {}, children: [], className: "", attributes: {},
            append(...children) { this.children.push(...children); },
            setAttribute(name, value) { this.attributes[name] = value; },
            remove() {}
          };
        }
      };
      const descendants = (node) => node.children.flatMap((child) => [child, ...descendants(child)]);
      const controller = new ConversationController(document, {});
      controller.element = element;
      controller.agentRunning = true;
      controller.refreshFocusedActivity();
      console.log(JSON.stringify(summaries.map((summary) => {
        const children = descendants(summary);
        const toggle = children.find((child) => child.dataset.focusActivityToggle);
        const details = children.find((child) => child.className === "focus-activity-details");
        return {
          tagName: summary.tagName,
          running: summary.className.includes("is-running"),
          spinner: children.some((child) => child.className === "focus-activity-spinner"),
          text: children.find((child) => child.className === "focus-activity-summary-text")?.textContent,
          items: children.filter((child) => child.tagName === "LI").map((child) => descendants(child).find((item) => item.className === "focus-activity-item-text")?.textContent),
          expandable: !!toggle,
          expanded: toggle?.attributes["aria-expanded"],
          listHidden: details?.hidden
        };
      })));
    JS

    assert_equal [
      { "tagName" => "SECTION", "running" => false, "spinner" => false, "text" => "1 reasoning step", "items" => [], "expandable" => false },
      { "tagName" => "SECTION", "running" => true, "spinner" => true, "text" => "1 tool update", "items" => ["bash mise run test"], "expandable" => true, "expanded" => "false", "listHidden" => true }
    ], results
  end

  def test_focused_activity_toggle_reveals_only_the_compact_list
    results = run_javascript(<<~JS)
      const { ConversationController } = await import(#{module_url("conversation_controller.js").to_json});
      const values = new Set();
      const details = { hidden: true };
      const summary = {
        classList: { toggle(name, enabled) { enabled ? values.add(name) : values.delete(name); } },
        querySelector: () => details
      };
      const toggle = {
        attributes: { "aria-expanded": "false" },
        closest: () => summary,
        getAttribute(name) { return this.attributes[name]; },
        setAttribute(name, value) { this.attributes[name] = value; }
      };
      const controller = new ConversationController({}, {});
      controller.updateJumpControls = () => {};
      controller.toggleFocusedActivity(toggle);
      const expanded = { aria: toggle.attributes["aria-expanded"], listHidden: details.hidden, classActive: values.has("is-expanded") };
      controller.toggleFocusedActivity(toggle);
      console.log(JSON.stringify({ expanded, collapsed: { aria: toggle.attributes["aria-expanded"], listHidden: details.hidden, classActive: values.has("is-expanded") } }));
    JS

    assert_equal({ "aria" => "true", "listHidden" => false, "classActive" => true }, results.fetch("expanded"))
    assert_equal({ "aria" => "false", "listHidden" => true, "classActive" => false }, results.fetch("collapsed"))
  end

  def test_live_text_updates_do_not_rebuild_focused_activity_groups
    results = run_javascript(<<~JS)
      const { ConversationController } = await import(#{module_url("conversation_controller.js").to_json});
      const controller = new ConversationController({}, {});
      controller.element = {};
      controller.autoScrollEnabled = false;
      let refreshes = 0;
      controller.scheduleFocusedActivityRefresh = () => { refreshes += 1; };
      controller.afterLiveOutputChange(false, true, false);
      controller.afterLiveOutputChange(false, true, true);
      console.log(JSON.stringify({ refreshes }));
    JS

    assert_equal 1, results.fetch("refreshes")
  end

  def test_live_auto_scroll_does_not_cancel_the_focused_activity_refresh
    results = run_javascript(<<~JS)
      const { ConversationController } = await import(#{module_url("conversation_controller.js").to_json});
      const callbacks = new Map();
      const cancelled = new Set();
      let nextFrame = 0;
      globalThis.requestAnimationFrame = (callback) => { const id = ++nextFrame; callbacks.set(id, callback); return id; };
      globalThis.cancelAnimationFrame = (id) => cancelled.add(id);
      const controller = new ConversationController({}, {});
      controller.element = {};
      controller.autoScrollEnabled = true;
      let refreshes = 0;
      let autoScrolls = 0;
      controller.refreshFocusedActivity = () => { refreshes += 1; };
      controller.applyAutoScroll = () => { autoScrolls += 1; };

      controller.afterLiveOutputChange(true, true, true);
      controller.afterLiveOutputChange(true, true, true);
      while (callbacks.size > 0) {
        const pending = [...callbacks.entries()];
        callbacks.clear();
        pending.forEach(([id, callback]) => { if (!cancelled.has(id)) callback(); });
      }
      console.log(JSON.stringify({ refreshes, autoScrolls, refreshPending: controller.focusedActivityRefreshFrame !== null }));
    JS

    assert_equal 1, results.fetch("refreshes")
    assert_equal 1, results.fetch("autoScrolls")
    assert_equal false, results.fetch("refreshPending")
  end

  def test_conversation_view_switch_preserves_the_visible_message_offset
    results = run_javascript(<<~JS)
      const { ConversationController } = await import(#{module_url("conversation_controller.js").to_json});
      class ClassList {
        constructor() { this.values = new Set(); }
        toggle(name, enabled) { enabled ? this.values.add(name) : this.values.delete(name); }
        contains(name) { return this.values.has(name); }
        remove(name) { this.values.delete(name); }
      }
      class ViewSelect {
        constructor() { this.value = "full"; this.listeners = []; }
        addEventListener(type, listener) { if (type === "change") this.listeners.push(listener); }
        removeEventListener(type, listener) { if (type === "change") this.listeners = this.listeners.filter((item) => item !== listener); }
        closest() { return null; }
        select(value) { this.value = value; this.listeners.forEach((listener) => listener()); }
        dispatchEvent() {}
      }
      const panel = { classList: new ClassList() };
      const viewSelect = new ViewSelect();
      const scroll = {
        _scrollTop: 0,
        clientHeight: 400,
        get scrollTop() { return this._scrollTop; },
        set scrollTop(value) { this._scrollTop = Math.min(Math.max(0, value), this.scrollHeight - this.clientHeight); },
        get scrollHeight() { return panel.classList.contains("is-conversation-focused") ? 1_600 : 2_300; },
        addEventListener() {}, removeEventListener() {}, querySelector: () => null,
        getBoundingClientRect: () => ({ top: 100, bottom: 500 }),
        querySelectorAll(selector) { return selector === ".message" ? [anchor] : []; }
      };
      const anchor = {
        dataset: { role: "assistant" },
        classList: { contains: (name) => name === "message--assistant" },
        getBoundingClientRect() {
          const absoluteTop = panel.classList.contains("is-conversation-focused") ? 750 : 1_450;
          return { top: absoluteTop - scroll.scrollTop + 100, bottom: absoluteTop - scroll.scrollTop + 300 };
        }
      };
      const document = {
        body: { classList: new ClassList() },
        getElementById: (id) => id === "conversation-scroll" ? scroll : null,
        querySelector(selector) {
          if (selector === ".conversation-panel") return panel;
          if (selector === "[data-conversation-view-select]") return viewSelect;
          return null;
        }
      };
      const window = { Event: class {}, location: { search: "", origin: "https://example.test" }, matchMedia: () => ({ matches: false }) };
      const controller = new ConversationController(document, window);
      controller.focusedView = true;
      controller.bind();
      scroll.scrollTop = 600;
      const offsetBefore = anchor.getBoundingClientRect().top - scroll.getBoundingClientRect().top;

      viewSelect.select("full");
      const expanded = {
        focused: panel.classList.contains("is-conversation-focused"),
        offset: anchor.getBoundingClientRect().top - scroll.getBoundingClientRect().top,
        scrollTop: scroll.scrollTop
      };
      viewSelect.select("conversation");
      const condensed = {
        focused: panel.classList.contains("is-conversation-focused"),
        offset: anchor.getBoundingClientRect().top - scroll.getBoundingClientRect().top,
        scrollTop: scroll.scrollTop
      };
      scroll.scrollTop = scroll.scrollHeight - scroll.clientHeight - 60;
      viewSelect.select("full");

      console.log(JSON.stringify({
        offsetBefore,
        expanded,
        condensed,
        bottom: {
          scrollTop: scroll.scrollTop,
          maximumScrollTop: scroll.scrollHeight - scroll.clientHeight,
          autoScrollEnabled: controller.autoScrollEnabled,
          followsOversizedMessage: controller.followOversizedMessageBottom
        }
      }));
    JS

    assert_equal 150, results.fetch("offsetBefore")
    assert_equal({ "focused" => false, "offset" => 150, "scrollTop" => 1_300 }, results.fetch("expanded"))
    assert_equal({ "focused" => true, "offset" => 150, "scrollTop" => 600 }, results.fetch("condensed"))
    assert_equal({ "scrollTop" => 1_900, "maximumScrollTop" => 1_900, "autoScrollEnabled" => true, "followsOversizedMessage" => true }, results.fetch("bottom"))
  end

  def test_conversation_view_selection_applies_on_one_change_event_and_survives_in_page_session_switching
    results = run_javascript(<<~JS)
      const { ConversationController } = await import(#{module_url("conversation_controller.js").to_json});
      class ClassList {
        constructor() { this.values = new Set(); }
        add(name) { this.values.add(name); }
        remove(name) { this.values.delete(name); }
        toggle(name, enabled) { enabled ? this.add(name) : this.remove(name); }
        contains(name) { return this.values.has(name); }
      }
      class ViewSelect {
        constructor() { this.value = "full"; this.listeners = []; this.trigger = { focus() {} }; }
        addEventListener(type, listener) { if (type === "change") this.listeners.push(listener); }
        removeEventListener(type, listener) { if (type === "change") this.listeners = this.listeners.filter((item) => item !== listener); }
        closest() { return { _projectSelectState: { trigger: this.trigger } }; }
        select(value) { this.value = value; this.listeners.forEach((listener) => listener()); }
      }
      const scroll = {
        scrollTop: 0, scrollHeight: 100, clientHeight: 100,
        addEventListener() {}, removeEventListener() {}, querySelectorAll: () => [], querySelector: () => null
      };
      let panel = { classList: new ClassList() };
      let viewSelect = new ViewSelect();
      const document = {
        body: { classList: new ClassList() },
        getElementById: (id) => id === "conversation-scroll" ? scroll : null,
        querySelector(selector) {
          if (selector === ".conversation-panel") return panel;
          if (selector === "[data-conversation-view-select]") return viewSelect;
          return null;
        }
      };
      const window = { Event: class { constructor(type) { this.type = type; } }, location: { search: "", origin: "https://example.test" }, matchMedia: () => ({ matches: false }) };
      const controller = new ConversationController(document, window);
      controller.bind();
      const initialView = viewSelect.value;
      const initialControlUsesTrigger = controller.viewSelectControl === viewSelect.trigger;
      viewSelect.select("conversation");
      const firstPanelFocused = panel.classList.contains("is-conversation-focused");
      const selectedView = viewSelect.value;

      controller.reset();
      panel = { classList: new ClassList() };
      viewSelect = new ViewSelect();
      controller.bind();
      const switchedPanelFocused = panel.classList.contains("is-conversation-focused");
      const switchedView = viewSelect.value;

      controller.reset();
      const reloadedController = new ConversationController(document, window);
      reloadedController.bind();
      console.log(JSON.stringify({
        initialView,
        initialControlUsesTrigger,
        firstPanelFocused,
        selectedView,
        switchedPanelFocused,
        switchedView,
        reloadedPanelFocused: panel.classList.contains("is-conversation-focused"),
        reloadedView: viewSelect.value
      }));
    JS

    assert_equal "full", results.fetch("initialView")
    assert_equal true, results.fetch("initialControlUsesTrigger")
    assert_equal true, results.fetch("firstPanelFocused")
    assert_equal "conversation", results.fetch("selectedView")
    assert_equal true, results.fetch("switchedPanelFocused")
    assert_equal "conversation", results.fetch("switchedView")
    assert_equal false, results.fetch("reloadedPanelFocused")
    assert_equal "full", results.fetch("reloadedView")
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
