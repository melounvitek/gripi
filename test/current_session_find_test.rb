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
      const first = bar("first"); const second = bar("second"); let current = first;
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

  def test_find_temporarily_reveals_only_selected_collapsed_tool_output
    results = run_javascript(<<~JS)
      const { CurrentSessionFindController } = await import(#{module_url("current_session_find_controller.js").to_json});
      const controller = new CurrentSessionFindController({}, {});
      const makeNode = (name) => ({ name, cloneNode() { return makeNode(this.name); } });
      function makeCollapse(name) {
        const body = { dataset: {}, childNodes: [makeNode(`${name}-tail`)], isConnected: true, replaceChildren(...nodes) { this.childNodes = nodes; } };
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
      const moved = [first.collapse.dataset.collapsed, first.body.childNodes[0].name, second.collapse.dataset.collapsed, second.body.childNodes[0].name];
      controller.restoreToolOutput();
      console.log(JSON.stringify([moved, second.collapse.dataset.collapsed, second.body.childNodes[0].name]));
    JS

    assert_equal [["true", "first-tail", "false", "second-full"], "true", "second-tail"], results
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
