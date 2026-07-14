require "minitest/autorun"
require "json"
require "open3"

class CurrentSessionFindTest < Minitest::Test
  ASSETS = File.expand_path("../public/assets", __dir__)
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

  def test_literal_matching_is_case_insensitive_and_does_not_treat_query_as_a_pattern
    results = run_javascript(<<~JS)
      const { CurrentSessionFindController } = await import(#{module_url("current_session_find_controller.js").to_json});
      const controller = new CurrentSessionFindController({}, {});
      console.log(JSON.stringify([
        controller.ranges("Alpha ALPHA a.lpha .", "alpha"),
        controller.ranges("Alpha ALPHA a.lpha .", ".")
      ]));
    JS

    assert_equal [[{"start" => 0, "end" => 5}, {"start" => 6, "end" => 11}], [{"start" => 13, "end" => 14}, {"start" => 19, "end" => 20}]], results
  end

  def test_find_waits_for_three_characters_before_loading_complete_history
    results = run_javascript(<<~JS)
      const { CurrentSessionFindController } = await import(#{module_url("current_session_find_controller.js").to_json});
      const listeners = {};
      const input = { value: "", addEventListener(type, listener) { listeners[type] = listener; }, focus() {}, select() {} };
      const count = { textContent: "" };
      const bar = {
        hidden: true,
        querySelector(selector) {
          if (selector.includes("input]")) return input;
          if (selector.includes("count]")) return count;
          return { addEventListener() {} };
        }
      };
      let loads = 0; let refreshes = 0;
      const conversation = {
        element: { querySelectorAll() { return []; } }, bindingEpoch: 1, olderHistoryLoading: false,
        async loadOlderHistory() { loads += 1; return "complete"; }
      };
      const controller = new CurrentSessionFindController({ querySelector: () => bar }, conversation);
      controller.refresh = () => { refreshes += 1; count.textContent = input.value; };
      controller.bind();
      await controller.show();
      const opened = { loads, count: count.textContent };
      input.value = "ab"; listeners.input(); await Promise.resolve();
      const short = { loads, count: count.textContent };
      input.value = "abc"; listeners.input(); await new Promise((resolve) => setTimeout(resolve, 0));
      const ready = { loads, refreshes, count: count.textContent };
      input.value = "ab"; listeners.input(); await Promise.resolve();
      const shortAfterComplete = { loads, count: count.textContent };
      input.value = "abc"; listeners.input(); await Promise.resolve();
      console.log(JSON.stringify({ opened, short, ready, shortAfterComplete, readyAgain: { loads, refreshes, count: count.textContent } }));
    JS

    assert_equal({ "loads" => 0, "count" => "Type 3+ characters" }, results.fetch("opened"))
    assert_equal({ "loads" => 0, "count" => "Type 3+ characters" }, results.fetch("short"))
    assert_equal({ "loads" => 1, "refreshes" => 1, "count" => "abc" }, results.fetch("ready"))
    assert_equal({ "loads" => 1, "count" => "Type 3+ characters" }, results.fetch("shortAfterComplete"))
    assert_equal({ "loads" => 1, "refreshes" => 2, "count" => "abc" }, results.fetch("readyAgain"))
  end

  def test_reopening_find_loads_pending_history_but_reuses_complete_history
    results = run_javascript(<<~JS)
      const { CurrentSessionFindController } = await import(#{module_url("current_session_find_controller.js").to_json});
      const input = { value: "", addEventListener() {}, focus() {}, select() {} };
      const count = { textContent: "" };
      const bar = {
        hidden: true,
        querySelector(selector) {
          if (selector.includes("input]")) return input;
          if (selector.includes("count]")) return count;
          return { addEventListener() {} };
        }
      };
      let loads = 0; let refreshes = 0;
      const conversation = {
        element: { querySelectorAll() { return []; }, focus() {} }, bindingEpoch: 1, olderHistoryLoading: false,
        async loadOlderHistory() { loads += 1; return "complete"; }
      };
      const controller = new CurrentSessionFindController({ querySelector: () => bar }, conversation);
      controller.refresh = () => { refreshes += 1; };
      controller.bind();
      await controller.show();
      controller.close();
      input.value = "abc"; await controller.show();
      const afterPending = { loads, refreshes };
      controller.close();
      await controller.show();
      console.log(JSON.stringify({ afterPending, afterComplete: { loads, refreshes } }));
    JS

    assert_equal({ "loads" => 1, "refreshes" => 1 }, results.fetch("afterPending"))
    assert_equal({ "loads" => 1, "refreshes" => 2 }, results.fetch("afterComplete"))
  end

  def test_shortening_find_query_clears_results_and_cancels_its_history_load
    results = run_javascript(<<~JS)
      const { CurrentSessionFindController } = await import(#{module_url("current_session_find_controller.js").to_json});
      async function scenario(shared) {
        const listeners = {};
        const input = { value: "abc", addEventListener(type, listener) { listeners[type] = listener; }, focus() {}, select() {} };
        const count = { textContent: "" };
        const bar = {
          hidden: true,
          querySelector(selector) {
            if (selector.includes("input]")) return input;
            if (selector.includes("count]")) return count;
            return { addEventListener() {} };
          }
        };
        let finish; let cancellations = 0; let refreshes = 0;
        const conversation = {
          element: { querySelectorAll() { return []; } }, bindingEpoch: 1, olderHistoryLoading: shared,
          loadOlderHistory() { return new Promise((resolve) => { finish = resolve; }); },
          cancelOlderHistory() { cancellations += 1; }
        };
        const controller = new CurrentSessionFindController({ querySelector: () => bar }, conversation);
        controller.refresh = () => { refreshes += 1; };
        controller.bind();
        const loading = controller.show();
        input.value = "ab"; listeners.input();
        finish("cancelled"); await loading;
        return { cancellations, refreshes, count: count.textContent, matches: controller.matches.length };
      }
      console.log(JSON.stringify({ owned: await scenario(false), shared: await scenario(true) }));
    JS

    expected = { "refreshes" => 0, "count" => "Type 3+ characters", "matches" => 0 }
    assert_equal expected.merge("cancellations" => 1), results.fetch("owned")
    assert_equal expected.merge("cancellations" => 0), results.fetch("shared")
  end

  def test_query_changed_during_history_loading_uses_latest_value
    results = run_javascript(<<~JS)
      const { CurrentSessionFindController } = await import(#{module_url("current_session_find_controller.js").to_json});
      const listeners = {};
      const input = { value: "first", addEventListener(type, listener) { listeners[type] = listener; }, focus() {}, select() {} };
      const count = { textContent: "" };
      const bar = {
        hidden: true,
        querySelector(selector) {
          if (selector.includes("input]")) return input;
          if (selector.includes("count]")) return count;
          return { addEventListener() {} };
        }
      };
      let finish; const refreshedQueries = [];
      const conversation = {
        element: { querySelectorAll() { return []; } }, bindingEpoch: 1, olderHistoryLoading: false,
        loadOlderHistory() { return new Promise((resolve) => { finish = resolve; }); }
      };
      const controller = new CurrentSessionFindController({ querySelector: () => bar }, conversation);
      controller.refresh = () => refreshedQueries.push(input.value);
      controller.bind();
      const loading = controller.show();
      input.value = "second"; listeners.input();
      finish("complete"); await loading;
      console.log(JSON.stringify(refreshedQueries));
    JS

    assert_equal ["second"], results
  end

  def test_cancelled_history_load_leaves_find_retryable
    results = run_javascript(<<~JS)
      const { CurrentSessionFindController } = await import(#{module_url("current_session_find_controller.js").to_json});
      const input = { value: "query", addEventListener() {}, focus() {}, select() {} };
      const count = { textContent: "" };
      const bar = {
        hidden: true,
        querySelector(selector) {
          if (selector.includes("input]")) return input;
          if (selector.includes("count]")) return count;
          return { addEventListener() {} };
        }
      };
      let loads = 0;
      const conversation = {
        element: { querySelectorAll() { return []; } }, bindingEpoch: 1, olderHistoryLoading: false,
        async loadOlderHistory() { loads += 1; return loads === 1 ? "cancelled" : "complete"; }
      };
      const controller = new CurrentSessionFindController({ querySelector: () => bar }, conversation);
      let refreshes = 0; controller.refresh = () => { refreshes += 1; };
      controller.bind();
      await controller.show();
      const cancelled = { count: count.textContent, status: controller.historyStatus };
      await controller.search();
      console.log(JSON.stringify({ cancelled, loads, refreshes }));
    JS

    assert_equal({ "cancelled" => { "count" => "History incomplete", "status" => "pending" }, "loads" => 2, "refreshes" => 1 }, results)
  end

  def test_find_renders_only_the_active_match
    results = run_javascript(<<~JS)
      const { CurrentSessionFindController } = await import(#{module_url("current_session_find_controller.js").to_json});
      const controller = new CurrentSessionFindController({}, { element: {} });
      controller.count = { textContent: "" };
      controller.matches = [{ root: {} }, { root: {} }, { root: {} }];
      controller.index = 0;
      let removals = 0;
      controller.removeHighlights = () => { removals += 1; };
      controller.restoreToolOutput = () => {};
      controller.revealToolOutput = () => false;
      controller.observe = () => {};
      controller.scrollMatchIntoView = () => {};
      const highlights = [];
      controller.highlight = () => highlights.push(controller.index);
      controller.renderHighlights(); controller.updateCount();
      const firstCount = controller.count.textContent;
      controller.move(1);
      console.log(JSON.stringify({ highlights, removals, firstCount, secondCount: controller.count.textContent }));
    JS

    assert_equal({ "highlights" => [0, 1], "removals" => 2, "firstCount" => "1 / 3", "secondCount" => "2 / 3" }, results)
  end

  def test_find_removes_only_the_active_highlights
    results = run_javascript(<<~JS)
      const { CurrentSessionFindController } = await import(#{module_url("current_session_find_controller.js").to_json});
      const conversation = { element: { querySelectorAll() { throw new Error("scanned conversation"); } } };
      const controller = new CurrentSessionFindController({}, conversation);
      let replacements = 0; let normalizations = 0;
      const parent = { normalize() { normalizations += 1; } };
      const mark = () => ({ parentNode: parent, childNodes: [{}], replaceWith() { replacements += 1; } });
      const match = { elements: [mark(), mark()] };
      controller.highlightedMatch = match;
      controller.removeHighlights();
      console.log(JSON.stringify({ replacements, normalizations, elements: match.elements.length, tracked: controller.highlightedMatch }));
    JS

    assert_equal({ "replacements" => 2, "normalizations" => 2, "elements" => 0, "tracked" => nil }, results)
  end

  def test_prepend_older_history_preserves_viewport_anchor
    results = run_javascript(<<~JS)
      const { ConversationController } = await import(#{module_url("conversation_controller.js").to_json});
      const template = { content: { querySelectorAll() { return []; } }, set innerHTML(_value) {} };
      const document = { createElement: () => template };
      const controller = new ConversationController(document, {});
      const scroll = {
        scrollTop: 140, scrollHeight: 500, firstElementChild: {},
        querySelector() { return { name: "first-message" }; },
        insertBefore(_content, point) { this.point = point.name; this.scrollHeight = 680; }
      };
      controller.element = scroll;
      controller.liveOutput = { name: "live" };
      controller.updateJumpControls = () => {};
      controller.prependOlderHtml("<article>older</article>");
      console.log(JSON.stringify({ scrollTop: scroll.scrollTop, lastScrollTop: controller.lastScrollTop, point: scroll.point }));
    JS

    assert_equal({ "scrollTop" => 320, "lastScrollTop" => 320, "point" => "first-message" }, results)
  end

  def test_older_window_fetches_only_one_chunk
    results = run_javascript(<<~JS)
      const { ConversationController } = await import(#{module_url("conversation_controller.js").to_json});
      const scroll = { dataset: { olderMessageCursor: "300", hasOlderMessages: "true", olderMessagesUrl: "/older" }, querySelector: () => null };
      const document = { querySelector: () => ({ value: "/session" }) };
      const controller = new ConversationController(document, { location: { origin: "https://example.test", search: "" } });
      controller.element = scroll; controller.bindingEpoch = 1; controller.prependOlderHtml = () => {}; controller.loadingHistoryStatus = () => {}; controller.availableHistoryStatus = () => {};
      let fetchCount = 0;
      globalThis.fetch = async () => { fetchCount += 1; return { ok: true, json: async () => ({ html: "older", next_cursor: 150, has_older_messages: true, older_message_count: 150 }) }; };
      const status = await controller.loadOlderWindow();
      console.log(JSON.stringify({ status, fetchCount, cursor: scroll.dataset.olderMessageCursor, hasOlder: scroll.dataset.hasOlderMessages }));
    JS

    assert_equal({ "status" => "more", "fetchCount" => 1, "cursor" => "150", "hasOlder" => "true" }, results)
  end

  def test_complete_history_load_fetches_all_remaining_messages_once
    results = run_javascript(<<~JS)
      const { ConversationController } = await import(#{module_url("conversation_controller.js").to_json});
      const scroll = { dataset: { olderMessageCursor: "300", hasOlderMessages: "true", olderMessagesUrl: "/older" }, querySelector: () => null };
      const document = { querySelector: () => ({ value: "/session" }) };
      const controller = new ConversationController(document, { location: { origin: "https://example.test", search: "" } });
      controller.element = scroll; controller.bindingEpoch = 1; controller.prependOlderHtml = () => {}; controller.loadingHistoryStatus = () => {}; controller.availableHistoryStatus = () => {}; controller.finishHistoryStatus = () => {};
      let fetchCount = 0; const urls = [];
      globalThis.fetch = async (url) => {
        fetchCount += 1; urls.push(url.toString());
        return { ok: true, json: async () => ({ html: "older", next_cursor: 0, has_older_messages: false, older_message_count: 0 }) };
      };
      const status = await controller.loadOlderHistory();
      console.log(JSON.stringify({ status, fetchCount, cursor: scroll.dataset.olderMessageCursor, hasOlder: scroll.dataset.hasOlderMessages, loadAll: urls[0].includes("all=1") }));
    JS

    assert_equal({ "status" => "complete", "fetchCount" => 1, "cursor" => "0", "hasOlder" => "false", "loadAll" => true }, results)
  end

  def test_complete_history_load_replaces_an_in_flight_window_request
    results = run_javascript(<<~JS)
      const { ConversationController } = await import(#{module_url("conversation_controller.js").to_json});
      const statusElement = { hidden: false, disabled: false, textContent: "" };
      const scroll = { dataset: { olderMessageCursor: "300", hasOlderMessages: "true", olderMessagesUrl: "/older" }, querySelector: () => statusElement };
      const document = { querySelector: () => ({ value: "/session" }) };
      const controller = new ConversationController(document, { location: { origin: "https://example.test", search: "" } });
      controller.element = scroll; controller.bindingEpoch = 1; controller.prependOlderHtml = () => {};
      let fetchCount = 0; let firstAborted = false; const urls = [];
      globalThis.fetch = (url, options) => {
        fetchCount += 1; urls.push(url.toString());
        if (fetchCount === 1) return new Promise((_resolve, reject) => options.signal.addEventListener("abort", () => { firstAborted = true; reject({ name: "AbortError" }); }));
        return Promise.resolve({ ok: true, json: async () => ({ html: "older", next_cursor: 0, has_older_messages: false, older_message_count: 0 }) });
      };
      const windowLoad = controller.loadOlderWindow();
      const historyLoad = controller.loadOlderHistory();
      console.log(JSON.stringify({ statuses: await Promise.all([windowLoad, historyLoad]), fetchCount, firstAborted, loadAll: urls[1].includes("all=1") }));
    JS

    assert_equal({ "statuses" => ["cancelled", "complete"], "fetchCount" => 2, "firstAborted" => true, "loadAll" => true }, results)
  end

  def test_scrolling_near_top_requests_one_older_window
    results = run_javascript(<<~JS)
      const { ConversationController } = await import(#{module_url("conversation_controller.js").to_json});
      const controller = new ConversationController({ body: { classList: { contains: () => false } } }, {});
      controller.element = { scrollTop: 40 };
      controller.lastScrollTop = 80;
      controller.nearBottom = () => false;
      controller.updateJumpControlsReveal = () => {};
      controller.updateJumpControls = () => {};
      let calls = 0;
      controller.loadOlderWindow = async () => { calls += 1; return "more"; };
      controller.handleScroll();
      await Promise.resolve();
      console.log(JSON.stringify({ calls, direction: controller.scrollDirection }));
    JS

    assert_equal({ "calls" => 1, "direction" => "up" }, results)
  end

  def test_history_status_can_load_an_underfilled_conversation
    results = run_javascript(<<~JS)
      const { ConversationController } = await import(#{module_url("conversation_controller.js").to_json});
      class Target {
        constructor() { this.listeners = {}; this.dataset = {}; this.scrollTop = 0; }
        addEventListener(type, listener) { this.listeners[type] = listener; }
        removeEventListener() {}
        querySelector(selector) { return selector.includes("history-status") ? status : null; }
      }
      const status = new Target();
      const scroll = new Target();
      const document = {
        body: { classList: { remove() {} } },
        getElementById: (id) => id === "conversation-scroll" ? scroll : null,
        querySelector: (selector) => selector.includes("input") ? { value: "/session" } : null
      };
      const controller = new ConversationController(document, { matchMedia: () => ({ matches: false }) });
      let calls = 0;
      controller.loadOlderWindow = async () => { calls += 1; return "more"; };
      controller.bind();
      status.listeners.click();
      await Promise.resolve();
      console.log(JSON.stringify({ calls, clickable: typeof status.listeners.click === "function" }));
    JS

    assert_equal({ "calls" => 1, "clickable" => true }, results)
  end

  def test_jump_to_top_shows_loading_state_until_full_history_is_ready
    results = run_javascript(<<~JS)
      const { ConversationController } = await import(#{module_url("conversation_controller.js").to_json});
      class Classes {
        constructor() { this.values = new Set(); }
        add(value) { this.values.add(value); }
        remove(value) { this.values.delete(value); }
        contains(value) { return this.values.has(value); }
        toggle(value, enabled) { enabled ? this.add(value) : this.remove(value); }
      }
      class Target {
        constructor() { this.listeners = {}; this.dataset = {}; this.classList = new Classes(); this.attributes = {}; this.disabled = false; this.scrollTop = 0; }
        addEventListener(type, listener) { this.listeners[type] = listener; }
        removeEventListener() {}
        setAttribute(name, value) { this.attributes[name] = String(value); }
        removeAttribute(name) { delete this.attributes[name]; }
        querySelector() { return null; }
      }
      const scroll = new Target(); const button = new Target(); const bottomButton = new Target(); const topControls = new Target();
      const document = {
        body: { classList: new Classes() },
        getElementById: (id) => id === "conversation-scroll" ? scroll : null,
        querySelector(selector) {
          if (selector === ".jump-controls--top") return topControls;
          if (selector === ".jump-to-first") return button;
          if (selector === ".jump-to-latest") return bottomButton;
          if (selector.includes("input")) return { value: "/session" };
          return null;
        }
      };
      const controller = new ConversationController(document, { matchMedia: () => ({ matches: false }) });
      let finish; let topBehavior; let bottomJump;
      controller.loadOlderHistory = () => new Promise((resolve) => { finish = resolve; });
      controller.scrollToTop = (behavior) => { topBehavior = behavior; };
      controller.scrollToBottom = (behavior, options) => { bottomJump = { behavior, force: options.force }; };
      controller.bind();
      let messageTarget = true;
      controller.updateJumpControls = () => controller.setJumpButton(button, messageTarget ? "message" : "conversation", messageTarget ? "↑" : "↑↑", messageTarget ? "Message top" : "Top");
      button.dataset.jumpTarget = "conversation";
      button.listeners.click();
      controller.updateJumpControls();
      const loading = { button: button.classList.contains("is-loading"), controls: topControls.classList.contains("is-loading"), disabled: button.disabled, busy: button.attributes["aria-busy"], label: button.attributes["aria-label"] };
      messageTarget = false;
      finish("complete");
      await new Promise((resolve) => setTimeout(resolve, 0));
      const restored = { button: !button.classList.contains("is-loading"), controls: !topControls.classList.contains("is-loading"), enabled: !button.disabled, busy: button.attributes["aria-busy"] === undefined, label: button.attributes["aria-label"] };
      bottomButton.dataset.jumpTarget = "conversation";
      bottomButton.listeners.click();
      console.log(JSON.stringify({ loading, restored, topBehavior, bottomJump }));
    JS

    assert_equal(
      {
        "loading" => { "button" => true, "controls" => true, "disabled" => true, "busy" => "true", "label" => "Loading earlier messages" },
        "restored" => { "button" => true, "controls" => true, "enabled" => true, "busy" => true, "label" => "Top" },
        "topBehavior" => "auto",
        "bottomJump" => { "behavior" => "auto", "force" => true }
      },
      results
    )
  end

  def test_stale_history_response_is_ignored_after_rebinding
    results = run_javascript(<<~JS)
      const { ConversationController } = await import(#{module_url("conversation_controller.js").to_json});
      class Target {
        constructor(name) { this.name = name; this.dataset = { olderMessageCursor: "1", hasOlderMessages: "true", olderMessagesUrl: "/older" }; this.insertions = 0; }
        addEventListener() {} removeEventListener() {}
        querySelector() { return null; } querySelectorAll() { return []; }
      }
      const first = new Target("first");
      const second = new Target("second");
      let current = first;
      const document = {
        body: { classList: { contains: () => false, add() {}, remove() {} } },
        getElementById: (id) => id === "conversation-scroll" ? current : null,
        querySelector: (selector) => selector.includes("input") ? { value: "/session" } : null
      };
      const window = { location: { origin: "https://example.test", search: "" }, matchMedia: () => ({ matches: false }) };
      let resolveFetch;
      globalThis.fetch = (_url, options) => new Promise((resolve) => { resolveFetch = resolve; first.signal = options.signal; });
      globalThis.requestAnimationFrame = () => 1; globalThis.cancelAnimationFrame = () => {};
      const controller = new ConversationController(document, window);
      controller.prependOlderHtml = () => { first.insertions += 1; };
      controller.bind();
      const loading = controller.loadOlderHistory();
      current = second;
      controller.bind();
      resolveFetch({ ok: true, json: async () => ({ html: "stale", next_cursor: 0, has_older_messages: false }) });
      console.log(JSON.stringify({ status: await loading, insertions: first.insertions, aborted: first.signal.aborted, epoch: controller.bindingEpoch }));
    JS

    assert_equal({ "status" => "cancelled", "insertions" => 0, "aborted" => true, "epoch" => 2 }, results)
  end

  def test_concurrent_history_callers_share_the_complete_load
    results = run_javascript(<<~JS)
      const { ConversationController } = await import(#{module_url("conversation_controller.js").to_json});
      const scroll = { dataset: { olderMessageCursor: "1", hasOlderMessages: "true", olderMessagesUrl: "/older" }, querySelector: () => null };
      const document = { querySelector: () => ({ value: "/session" }) };
      const controller = new ConversationController(document, { location: { origin: "https://example.test", search: "" } });
      controller.element = scroll; controller.bindingEpoch = 4; controller.prependOlderHtml = () => {}; controller.finishHistoryStatus = () => {};
      let fetchCount = 0; let finishFetch;
      globalThis.fetch = () => { fetchCount += 1; return new Promise((resolve) => { finishFetch = () => resolve({ ok: true, json: async () => ({ html: "", next_cursor: 0, has_older_messages: false, older_message_count: 0 }) }); }); };
      const first = controller.loadOlderHistory();
      const second = controller.loadOlderHistory();
      const shared = first === second;
      finishFetch();
      console.log(JSON.stringify({ shared, fetchCount, statuses: await Promise.all([first, second]), pending: controller.olderHistoryPromise !== null }));
    JS

    assert_equal({ "shared" => true, "fetchCount" => 1, "statuses" => ["complete", "complete"], "pending" => false }, results)
  end

  def test_stale_find_preparation_cannot_update_replacement_session
    results = run_javascript(<<~JS)
      const { CurrentSessionFindController } = await import(#{module_url("current_session_find_controller.js").to_json});
      function field() { return { addEventListener() {}, focus() {}, select() {}, textContent: "", hidden: false }; }
      function bar(name) {
        const input = field(); const count = field();
        const result = { name, hidden: true, querySelector(selector) { if (selector.includes("input]")) return input; if (selector.includes("count]")) return count; return field(); } };
        return { result, input, count };
      }
      const first = bar("first"); const second = bar("second"); first.input.value = "first"; second.input.value = "second"; let current = first;
      const document = { querySelector: () => current.result };
      const requests = [];
      const conversationElement = () => ({ focus() {}, querySelectorAll() { return []; } });
      const conversation = { element: conversationElement(), bindingEpoch: 1, loadOlderHistory: () => new Promise((resolve) => requests.push(resolve)) };
      globalThis.cancelAnimationFrame = () => {};
      const controller = new CurrentSessionFindController(document, conversation);
      controller.refresh = () => { controller.count.textContent = "refreshed"; };
      controller.bind();
      const oldPreparation = controller.show();
      current = second; conversation.element = conversationElement(); conversation.bindingEpoch += 1;
      controller.bind();
      const newPreparation = controller.show();
      requests[0]("complete"); await oldPreparation;
      const afterOld = second.count.textContent;
      requests[1]("complete"); await newPreparation;
      console.log(JSON.stringify({ afterOld, afterNew: second.count.textContent, firstCount: first.count.textContent, epoch: controller.bindingEpoch }));
    JS

    assert_equal "Loading…", results.fetch("afterOld")
    assert_equal "refreshed", results.fetch("afterNew")
    assert_equal "0 / 0", results.fetch("firstCount")
    assert_equal 2, results.fetch("epoch")
  end

  def test_closing_find_cancels_its_full_history_load
    results = run_javascript(<<~JS)
      const { CurrentSessionFindController } = await import(#{module_url("current_session_find_controller.js").to_json});
      let cancellations = 0;
      const conversation = { element: { querySelectorAll: () => [], focus() {} }, cancelOlderHistory: () => { cancellations += 1; } };
      const controller = new CurrentSessionFindController({}, conversation);
      controller.preparationPromise = Promise.resolve();
      controller.ownsHistoryLoad = true;
      controller.close({ restoreFocus: false });
      console.log(JSON.stringify({ cancellations, pending: controller.preparationPromise !== null }));
    JS

    assert_equal({ "cancellations" => 1, "pending" => false }, results)
  end

  def test_closing_find_keeps_a_shared_full_history_load_running
    results = run_javascript(<<~JS)
      const { CurrentSessionFindController } = await import(#{module_url("current_session_find_controller.js").to_json});
      let cancellations = 0;
      const conversation = { element: { querySelectorAll: () => [], focus() {} }, cancelOlderHistory: () => { cancellations += 1; } };
      const controller = new CurrentSessionFindController({}, conversation);
      controller.preparationPromise = Promise.resolve();
      controller.ownsHistoryLoad = false;
      controller.close({ restoreFocus: false });
      console.log(JSON.stringify({ cancellations, pending: controller.preparationPromise !== null }));
    JS

    assert_equal({ "cancellations" => 0, "pending" => false }, results)
  end

  def test_find_temporarily_reveals_only_selected_collapsed_tool_output
    results = run_javascript(<<~JS)
      const { CurrentSessionFindController } = await import(#{module_url("current_session_find_controller.js").to_json});
      const controller = new CurrentSessionFindController({}, {});
      const makeNode = (name) => ({ name, cloneNode() { return makeNode(this.name); } });
      function makeCollapse(name) {
        const body = { dataset: {}, childNodes: [makeNode(`${name}-tail`)], isConnected: true, attributes: {}, replaceChildren(...nodes) { this.childNodes = nodes; }, setAttribute(key, value) { this.attributes[key] = value; }, removeAttribute(key) { delete this.attributes[key]; } };
        const content = (suffix) => ({ childNodes: [makeNode(`${name}-${suffix}`)], cloneNode() { return { childNodes: this.childNodes.map((node) => node.cloneNode()) }; } });
        const control = { hidden: false }; const button = { value: "false", getAttribute() { return this.value; }, setAttribute(_name, value) { this.value = value; } };
        const elements = { "[data-tool-output-body]": body, "[data-tool-output-full]": { content: content("full") }, "[data-tool-output-tail]": { content: content("tail") }, "[data-tool-output-collapse-control]": control, "[data-tool-output-toggle]": button };
        const collapse = { dataset: { collapsed: "true" }, isConnected: true, querySelector: (selector) => elements[selector] };
        return { collapse, body };
      }
      const first = makeCollapse("first"); const second = makeCollapse("second");
      controller.revealToolOutput({ collapse: first.collapse });
      controller.restoreToolOutput(second.collapse);
      controller.revealToolOutput({ collapse: second.collapse });
      const moved = [first.collapse.dataset.collapsed, first.body.childNodes[0].name, second.collapse.dataset.collapsed, second.body.childNodes[0].name, second.body.tabIndex, second.body.attributes.role];
      controller.restoreToolOutput();
      console.log(JSON.stringify([moved, second.collapse.dataset.collapsed, second.body.childNodes[0].name, second.body.tabIndex, second.body.attributes.role || null]));
    JS

    assert_equal [["true", "first-tail", "false", "second-full", 0, "region"], "true", "second-tail", -1, nil], results
  end

  def test_selected_match_is_revealed_inside_expanded_tool_output
    results = run_javascript(<<~JS)
      const { CurrentSessionFindController } = await import(#{module_url("current_session_find_controller.js").to_json});
      let innerScroll = null;
      let outerScroll = null;
      const body = {
        scrollTop: 20, clientHeight: 100, scrollHeight: 400,
        getBoundingClientRect: () => ({ top: 100, bottom: 200 }),
        scrollTo(options) { innerScroll = options; this.scrollTop = options.top; }
      };
      const mark = {
        closest: (selector) => selector === "[data-tool-output-body]" ? body : null,
        getBoundingClientRect: () => ({ top: 240 - (body.scrollTop - 20), bottom: 250 - (body.scrollTop - 20), height: 10 })
      };
      const scroll = {
        scrollTop: 50, clientHeight: 500,
        getBoundingClientRect: () => ({ top: 0, bottom: 500 }),
        scrollTo(options) { outerScroll = options; }
      };
      let stopped = false;
      const conversation = {
        element: scroll,
        stopAutoFollow() { stopped = true; },
        withProgrammaticScroll(callback) { callback(); }
      };
      const controller = new CurrentSessionFindController({}, conversation);
      controller.matches = [{ elements: [mark] }];
      controller.index = 0;
      controller.scrollMatchIntoView();
      const markRect = mark.getBoundingClientRect();
      console.log(JSON.stringify({ innerScroll, outerScroll, stopped, markTop: markRect.top, markBottom: markRect.bottom }));
    JS

    assert_equal({ "top" => 115, "behavior" => "auto" }, results["innerScroll"])
    assert_equal "smooth", results.dig("outerScroll", "behavior")
    assert_equal true, results["stopped"]
    assert_operator results["markTop"], :>=, 100
    assert_operator results["markBottom"], :<=, 200
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
