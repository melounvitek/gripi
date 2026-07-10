require "minitest/autorun"
require "json"
require "open3"

class CurrentSessionFindTest < Minitest::Test
  VIEW_PATH = File.expand_path("../views/index.erb", __dir__)
  CONVERSATION_PATH = File.expand_path("../views/_conversation.erb", __dir__)

  def test_selected_session_renders_hidden_find_controls
    conversation = File.read(CONVERSATION_PATH)

    assert_includes conversation, 'class="current-session-find" data-current-session-find hidden'
    assert_includes conversation, 'type="search" data-current-session-find-input'
    assert_includes conversation, 'data-current-session-find-count aria-live="polite">0 / 0'
    assert_includes conversation, 'data-current-session-find-previous'
    assert_includes conversation, 'data-current-session-find-next'
    assert_includes conversation, 'data-current-session-find-close'
    assert_includes conversation, 'type="checkbox" data-current-session-find-conversation-only'
    assert_includes conversation, 'Conversation only'
  end

  def test_document_shortcut_reserves_shift_find_and_ignores_alt_find
    results = run_javascript(<<~JS)
      let currentSessionFindBar = {};
      #{javascript_function("currentSessionFindShortcut")}
      console.log(JSON.stringify([
        currentSessionFindShortcut({ key: "f", ctrlKey: true }),
        currentSessionFindShortcut({ key: "F", metaKey: true }),
        currentSessionFindShortcut({ key: "f", ctrlKey: true, shiftKey: true }),
        currentSessionFindShortcut({ key: "f", metaKey: true, altKey: true }),
        currentSessionFindShortcut({ key: "f" })
      ]));
    JS

    assert_equal [true, true, false, false, false], results
    assert_includes File.read(VIEW_PATH), 'openCurrentSessionFind().catch(() => {});'
  end

  def test_find_navigation_shortcuts_move_only_an_open_find_bar
    results = run_javascript(<<~JS)
      const currentSessionFindBar = { hidden: false };
      const currentSessionFindOpen = () => !currentSessionFindBar.hidden;
      let modalOpen = false;
      const modalIsOpen = () => modalOpen;
      let moved = [];
      const moveCurrentSessionFind = (direction) => moved.push(direction);
      #{javascript_function("currentSessionFindNavigationShortcut")}
      #{javascript_function("requestCurrentSessionFindNavigation")}
      #{javascript_function("handleCurrentSessionFindNavigationShortcut")}
      const events = [
        { key: "g", ctrlKey: true },
        { key: "G", metaKey: true, shiftKey: true },
        { key: "g", ctrlKey: true, altKey: true },
        { key: "g" }
      ].map((event) => ({ ...event, prevented: false, preventDefault() { this.prevented = true; } }));
      const handled = events.map((event) => handleCurrentSessionFindNavigationShortcut(event));
      currentSessionFindBar.hidden = true;
      const closed = handleCurrentSessionFindNavigationShortcut({ key: "g", ctrlKey: true, preventDefault() { throw new Error("must not handle"); } });
      currentSessionFindBar.hidden = false;
      modalOpen = true;
      const modal = requestCurrentSessionFindNavigation(-1);
      console.log(JSON.stringify([handled, events.map((event) => event.prevented), moved, closed, modal]));
    JS

    assert_equal [[true, true, false, false], [true, true, false, false], [1, -1], false, false], results
    script = File.read(VIEW_PATH)
    assert_includes script, "if (handleCurrentSessionFindNavigationShortcut(event)) return;"
  end

  def test_concurrent_history_callers_share_the_complete_load
    results = run_javascript(<<~JS)
      let olderConversationLoadPromise = null;
      let sessionViewGeneration = 3;
      const conversationScroll = { dataset: { olderMessageCursor: "1", hasOlderMessages: "true" } };
      const currentSessionPath = () => "/session";
      const olderConversationUrl = () => "/older";
      const prependOlderConversationHtml = () => {};
      const finishConversationHistoryStatus = () => {};
      const failConversationHistoryStatus = () => {};
      let fetchCount = 0;
      let finishFetch;
      const fetch = () => {
        fetchCount += 1;
        return new Promise((resolve) => {
          finishFetch = () => resolve({
            ok: true,
            json: async () => ({ html: "", next_cursor: 0, has_older_messages: false, older_message_count: 0 })
          });
        });
      };
      #{javascript_function("loadOlderConversationHistory")}
      const first = loadOlderConversationHistory();
      const second = loadOlderConversationHistory();
      const shared = first === second;
      finishFetch();
      Promise.all([first, second]).then((statuses) => {
        console.log(JSON.stringify([shared, fetchCount, olderConversationLoadPromise === null, conversationScroll.dataset.hasOlderMessages, statuses]));
      });
    JS

    assert_equal [true, 1, true, "false", ["complete", "complete"]], results
    script = File.read(VIEW_PATH)
    assert_includes script, 'await loadOlderConversationHistory(generation);'
    assert_includes script, 'refreshCurrentSessionFindMatches();'
  end

  def test_history_load_reports_network_failure_without_failing_a_cancelled_view
    results = run_javascript(<<~JS)
      let olderConversationLoadPromise = null;
      let sessionViewGeneration = 3;
      const conversationScroll = { dataset: { olderMessageCursor: "1", hasOlderMessages: "true" } };
      const currentSessionPath = () => "/session";
      const olderConversationUrl = () => "/older";
      const prependOlderConversationHtml = () => {};
      const finishConversationHistoryStatus = () => {};
      let historyFailureCount = 0;
      const failConversationHistoryStatus = () => { historyFailureCount += 1; };
      let rejectNetwork = true;
      let finishFetch;
      const fetch = () => {
        if (rejectNetwork) return Promise.reject(new Error("offline"));
        return new Promise((resolve) => { finishFetch = () => resolve({ ok: false }); });
      };
      #{javascript_function("loadOlderConversationHistory")}
      (async () => {
        const failed = await loadOlderConversationHistory();
        rejectNetwork = false;
        const cancelledLoad = loadOlderConversationHistory();
        sessionViewGeneration += 1;
        finishFetch();
        const cancelled = await cancelledLoad;
        console.log(JSON.stringify([failed, cancelled, historyFailureCount]));
      })();
    JS

    assert_equal ["failed", "cancelled", 1], results
  end

  def test_failed_history_load_keeps_find_incomplete_and_retries_when_reopened
    results = run_javascript(<<~JS)
      let olderConversationLoadPromise = null;
      let currentSessionFindPreparationPromise = null;
      let currentSessionFindHistoryStatus = "complete";
      let currentSessionFindObserver = null;
      let currentSessionFindMatches = [{ partial: true }];
      let currentSessionFindIndex = 0;
      let sessionViewGeneration = 3;
      const currentSessionFindBar = { hidden: true };
      const currentSessionFindInput = {
        disabled: false,
        focus() {},
        select() {}
      };
      const currentSessionFindCount = { textContent: "0 / 0" };
      const conversationScroll = { dataset: { olderMessageCursor: "1", hasOlderMessages: "true" } };
      const currentSessionPath = () => "/session";
      const olderConversationUrl = () => "/older";
      const removeCurrentSessionFindHighlights = () => {};
      const restoreCurrentSessionFindToolOutput = () => {};
      const prependOlderConversationHtml = () => {};
      const finishConversationHistoryStatus = () => {};
      let historyFailureCount = 0;
      const failConversationHistoryStatus = () => { historyFailureCount += 1; };
      const loadingCounts = [];
      let fetchCount = 0;
      const fetch = async () => {
        fetchCount += 1;
        loadingCounts.push(currentSessionFindCount.textContent);
        if (fetchCount === 1) return { ok: false };
        return {
          ok: true,
          json: async () => ({ html: "", next_cursor: 0, has_older_messages: false, older_message_count: 0 })
        };
      };
      let refreshCount = 0;
      const refreshCurrentSessionFindMatches = () => {
        refreshCount += 1;
        currentSessionFindMatches = [{ complete: true }];
        currentSessionFindCount.textContent = "1 / 1";
      };
      #{javascript_function("loadOlderConversationHistory")}
      #{javascript_function("currentSessionFindOpen")}
      #{javascript_function("openCurrentSessionFind")}
      (async () => {
        await openCurrentSessionFind();
        const failedState = [refreshCount, currentSessionFindCount.textContent, currentSessionFindMatches.length, currentSessionFindInput.disabled];
        await openCurrentSessionFind();
        console.log(JSON.stringify([loadingCounts, fetchCount, historyFailureCount, failedState, refreshCount, currentSessionFindCount.textContent]));
      })();
    JS

    assert_equal [["Loading…", "Loading…"], 2, 1, [0, "History incomplete", 0, false], 1, "1 / 1"], results
  end

  def test_literal_matching_is_case_insensitive_and_does_not_treat_query_as_a_pattern
    results = run_javascript(<<~JS)
      #{javascript_function("escapeRegExp")}
      #{javascript_function("currentSessionFindRanges")}
      console.log(JSON.stringify([
        currentSessionFindRanges("Alpha ALPHA a.lpha .", "alpha"),
        currentSessionFindRanges("Alpha ALPHA a.lpha .", ".")
      ]));
    JS

    assert_equal [[{"start" => 0, "end" => 5}, {"start" => 6, "end" => 11}], [{"start" => 13, "end" => 14}, {"start" => 19, "end" => 20}]], results
  end

  def test_find_scopes_message_content_and_reveals_only_the_selected_collapsed_output
    results = run_javascript(<<~JS)
      let currentSessionFindExpandedToolOutput = null;
      const makeNode = (name) => ({ name, cloneNode() { return makeNode(this.name); } });
      const makeContent = (name) => ({
        childNodes: [makeNode(name)],
        cloneNode() { return { childNodes: this.childNodes.map((node) => node.cloneNode(true)) }; }
      });
      const makeCollapse = (name) => {
        const body = {
          dataset: {},
          childNodes: [makeNode(`${name}-tail`)],
          isConnected: true,
          replaceChildren(...nodes) { this.childNodes = nodes; }
        };
        const fullTemplate = { content: makeContent(`${name}-full`) };
        const tailTemplate = { content: makeContent(`${name}-tail`) };
        const control = { hidden: false };
        const button = {
          value: "false",
          getAttribute() { return this.value; },
          setAttribute(_name, value) { this.value = value; }
        };
        const elements = {
          "[data-tool-output-body]": body,
          "[data-tool-output-full]": fullTemplate,
          "[data-tool-output-tail]": tailTemplate,
          "[data-tool-output-collapse-control]": control,
          "[data-tool-output-toggle]": button
        };
        const collapse = {
          dataset: { collapsed: "true" },
          isConnected: true,
          querySelector(selector) { return elements[selector]; }
        };
        return { collapse, body, control, button };
      };
      #{javascript_function("restoreCurrentSessionFindToolOutput")}
      #{javascript_function("revealToolOutputForCurrentSessionFind")}
      const first = makeCollapse("first");
      const second = makeCollapse("second");
      revealToolOutputForCurrentSessionFind({ collapse: first.collapse });
      restoreCurrentSessionFindToolOutput(second.collapse);
      revealToolOutputForCurrentSessionFind({ collapse: second.collapse });
      const moved = [first.collapse.dataset.collapsed, first.body.childNodes[0].name, second.collapse.dataset.collapsed, second.body.childNodes[0].name];
      restoreCurrentSessionFindToolOutput();
      console.log(JSON.stringify([moved, second.collapse.dataset.collapsed, second.body.childNodes[0].name]));
    JS

    assert_equal [["true", "first-tail", "false", "second-full"], "true", "second-tail"], results
    script = File.read(VIEW_PATH)
    assert_includes script, 'message.querySelectorAll(".compact-summary, .message-body")'
    assert_includes script, 'currentSessionFindConversationMessage(message)'
    assert_includes script, 'template?.content'
    assert_includes script, 'body.dataset.rawText'
    assert_includes script, 'mark.className = "current-session-find-match";'
    assert_includes script, 'mark.classList.toggle("is-active", index === currentSessionFindIndex);'
  end

  def test_find_refreshes_after_live_dom_changes_and_clears_on_session_reset
    script = File.read(VIEW_PATH)

    assert_includes script, 'currentSessionFindObserver = new MutationObserver'
    assert_includes script, 'scheduleCurrentSessionFindRefresh();'
    assert_includes script, 'closeCurrentSessionFind({ restoreFocus: false });'
    assert_includes script, 'bindCurrentSessionFindControls();'
  end

  private

  def javascript_function(name)
    File.read(VIEW_PATH).match(/^    (?:async )?function #{Regexp.escape(name)}\b.*?^    }$/m)&.[](0) || flunk("Missing JavaScript function #{name}")
  end

  def run_javascript(source)
    stdout, stderr, status = Open3.capture3("node", "-e", source)
    assert status.success?, stderr
    JSON.parse(stdout)
  end
end
