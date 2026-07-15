require "minitest/autorun"
require "json"
require "open3"

class TreeSessionControllerJsTest < Minitest::Test
  ASSET_URL = "file://#{File.expand_path("../public/assets/tree_session_controller.js", __dir__)}"
  VIEW_PATH = File.expand_path("../views/_fork_session_modal.erb", __dir__)
  APP_PATH = File.expand_path("../public/assets/app.js", __dir__)

  def test_tree_model_searches_case_insensitive_and_tokens_and_reparents_matches
    result = run_javascript(<<~JS)
      const { TreeSessionModel } = await import(#{ASSET_URL.to_json});
      const model = new TreeSessionModel([
        { entryId: "root", parentId: null, text: "Build Release", role: "user" },
        { entryId: "answer", parentId: "root", text: "Linux release details", role: "assistant" },
        { entryId: "other", parentId: "root", text: "macOS release", role: "assistant" }
      ]);
      model.select("answer");
      model.setSearch("RELEASE linux");
      const structure = model.visibleStructure();
      console.log(JSON.stringify({ ids: structure.entries.map((entry) => entry.entryId), roots: structure.roots.map((entry) => entry.entryId), selected: model.selectedId }));
    JS

    assert_equal ["answer"], result.fetch("ids")
    assert_equal ["answer"], result.fetch("roots")
    assert_equal "answer", result.fetch("selected")
  end

  def test_tree_model_folds_and_supports_practical_tree_navigation
    result = run_javascript(<<~JS)
      const { TreeSessionModel } = await import(#{ASSET_URL.to_json});
      const model = new TreeSessionModel([
        { entryId: "root", parentId: null, text: "Root" },
        { entryId: "one", parentId: "root", text: "One" },
        { entryId: "leaf", parentId: "one", text: "Leaf" },
        { entryId: "two", parentId: "root", text: "Two" }
      ]);
      model.select("root");
      model.move("right");
      const firstChild = model.selectedId;
      model.move("right");
      const grandchild = model.selectedId;
      model.move("left");
      model.move("left");
      const folded = model.visibleEntries().map((entry) => entry.entryId);
      model.move("right");
      model.move("end");
      const end = model.selectedId;
      model.move("home");
      const home = model.selectedId;
      console.log(JSON.stringify({ firstChild, grandchild, folded, end, home }));
    JS

    assert_equal "one", result.fetch("firstChild")
    assert_equal "leaf", result.fetch("grandchild")
    assert_equal ["root", "one", "two"], result.fetch("folded")
    assert_equal "two", result.fetch("end")
    assert_equal "root", result.fetch("home")
  end

  def test_search_does_not_reveal_matches_below_a_folded_branch
    result = run_javascript(<<~JS)
      const { TreeSessionModel } = await import(#{ASSET_URL.to_json});
      const model = new TreeSessionModel([
        { entryId: "root", parentId: null, text: "Root" },
        { entryId: "child", parentId: "root", text: "Needle" }
      ]);
      model.collapsed.add("root");
      model.setSearch("needle");
      console.log(JSON.stringify(model.visibleEntries().map((entry) => entry.entryId)));
    JS

    assert_empty result
  end

  def test_controller_renders_treeitems_from_the_visible_structure
    result = run_javascript(<<~JS)
      const { TreeSessionController, TreeSessionModel } = await import(#{ASSET_URL.to_json});
      const makeNode = (tag) => ({
        tag, children: [], dataset: {}, attributes: {}, textContent: "", tabIndex: null,
        classList: { toggle() {} },
        setAttribute(name, value) { this.attributes[name] = value; },
        append(...children) { this.children.push(...children); },
        replaceChildren(...children) { this.children = children; }
      });
      const viewport = makeNode("ul");
      const modal = {
        querySelector: (selector) => selector === "[data-tree-viewport]" ? viewport : null,
        querySelectorAll: () => []
      };
      const document = { addEventListener() {}, querySelector: () => modal, createElement: makeNode };
      const controller = new TreeSessionController(document, {});
      controller.model = new TreeSessionModel([{ entryId: "entry-1", parentId: null, role: "user", text: "Prompt", current: true }]);
      controller.render();
      const item = viewport.children[0];
      console.log(JSON.stringify({ count: viewport.children.length, role: item.attributes.role, selected: item.attributes["aria-selected"] }));
    JS

    assert_equal 1, result.fetch("count")
    assert_equal "treeitem", result.fetch("role")
    assert_equal "true", result.fetch("selected")
  end

  def test_loading_a_different_session_resets_stale_selection
    result = run_javascript(<<~JS)
      const { TreeSessionController, TreeSessionModel } = await import(#{ASSET_URL.to_json});
      const list = { dataset: { treeEntriesUrl: "/sessions/tree_entries?session=new" }, setAttribute() {} };
      const filter = { value: "default" };
      const status = { textContent: "", classList: { toggle() {} } };
      const modal = {
        hidden: false,
        querySelector(selector) {
          return { "[data-tree-session-list]": list, "[data-tree-filter]": filter, "[data-tree-search]": { value: "" }, "[data-tree-session-status]": status }[selector] || null;
        }
      };
      const document = { addEventListener() {}, querySelector: () => modal };
      globalThis.fetch = async () => ({ ok: true, json: async () => ({
        entries: [
          { entryId: "shared", parentId: null },
          { entryId: "new-current", parentId: "shared", current: true }
        ],
        settings: {}, filter: "default"
      }) });
      const controller = new TreeSessionController(document, { location: { origin: "https://example.test" } });
      controller.model = new TreeSessionModel([{ entryId: "shared", current: true }]);
      controller.model.collapsed.add("shared");
      controller.treeUrl = "/sessions/tree_entries?session=old";
      controller.render = () => {};
      await controller.load(modal);
      console.log(JSON.stringify({ selected: controller.model.selectedId, collapsed: [...controller.model.collapsed] }));
    JS

    assert_equal "new-current", result.fetch("selected")
    assert_empty result.fetch("collapsed")
  end

  def test_summary_step_is_explicit_unless_native_settings_skip_it
    result = run_javascript(<<~JS)
      const { TreeSessionController, TreeSessionModel } = await import(#{ASSET_URL.to_json});
      const browser = { hidden: false };
      const summary = { hidden: true };
      const radio = { focused: false, focus() { this.focused = true; } };
      const modal = {
        hidden: false,
        querySelector(selector) {
          return {
            "[data-tree-browser-step]": browser,
            "[data-tree-summary-step]": summary,
            'input[name="summary_mode"]:checked': radio
          }[selector] || null;
        }
      };
      const document = { addEventListener() {}, querySelector: () => modal };
      const direct = [];
      const controller = new TreeSessionController(document, { location: { origin: "https://example.test" } });
      controller.model = new TreeSessionModel([{ entryId: "entry-1", current: true }]);
      controller.navigate = (...args) => direct.push(args);

      controller.settings = { branchSummary: { skipPrompt: false } };
      controller.requestNavigation();
      const prompted = { browserHidden: browser.hidden, summaryHidden: summary.hidden, focused: radio.focused };
      controller.showTreeStep();
      controller.settings = { branchSummary: { skipPrompt: true } };
      controller.requestNavigation();
      console.log(JSON.stringify({ prompted, direct }));
    JS

    assert_equal({ "browserHidden" => true, "summaryHidden" => false, "focused" => true }, result.fetch("prompted"))
    assert_equal [["none", ""]], result.fetch("direct")
  end

  def test_navigation_posts_selected_summary_and_custom_instructions
    result = run_javascript(<<~JS)
      const { TreeSessionController, TreeSessionModel } = await import(#{ASSET_URL.to_json});
      const submit = { disabled: false };
      const modal = { hidden: false, querySelector: (selector) => selector === "[data-tree-summary-submit]" ? submit : null };
      const document = { addEventListener() {}, querySelector: () => modal };
      const requests = [];
      globalThis.fetch = async (url, options) => {
        requests.push({ url, method: options.method, body: Object.fromEntries(options.body) });
        return { ok: true, json: async () => ({ session: "/session", redirect: "/" }) };
      };
      const events = [];
      const controller = new TreeSessionController(document, { location: { origin: "https://example.test" } }, {
        currentSessionPath: () => "/session",
        addSessionViewFormParams: (body) => body.set("project", "demo"),
        closeModal: () => events.push("closed"),
        navigate: async (_payload, entry) => events.push(`navigated:${entry.entryId}`),
        showSessionSwitching: () => events.push("show"),
        hideSessionSwitching: () => events.push("hide")
      });
      controller.model = new TreeSessionModel([{ entryId: "entry-1", current: true }]);
      await controller.navigate("custom", " Focus on tests ");
      console.log(JSON.stringify({ requests, events, submitDisabled: submit.disabled }));
    JS

    assert_equal [{
      "url" => "/sessions/tree", "method" => "POST",
      "body" => { "session" => "/session", "entry_id" => "entry-1", "summary_mode" => "custom", "custom_instructions" => "Focus on tests", "project" => "demo" }
    }], result.fetch("requests")
    assert_equal ["show", "closed", "navigated:entry-1", "hide"], result.fetch("events")
    assert_equal false, result.fetch("submitDisabled")
  end

  def test_label_set_and_clear_post_the_selected_entry_and_update_the_model
    result = run_javascript(<<~JS)
      const { TreeSessionController, TreeSessionModel } = await import(#{ASSET_URL.to_json});
      const status = { textContent: "", classList: { toggle() {} } };
      const modal = { querySelector: (selector) => selector === "[data-tree-session-status]" ? status : null };
      const document = { addEventListener() {}, querySelector: () => modal };
      const requests = [];
      globalThis.fetch = async (url, options) => {
        const body = Object.fromEntries(options.body);
        requests.push({ url, method: options.method, body });
        return { ok: true, json: async () => ({ entryId: body.entry_id, label: body.label || null }) };
      };
      const controller = new TreeSessionController(document, {}, { currentSessionPath: () => "/session" });
      controller.model = new TreeSessionModel([{ entryId: "entry-1", current: true, label: null }]);
      controller.render = () => {};
      let reloads = 0;
      controller.load = async () => { reloads += 1; };
      await controller.saveLabel("checkpoint");
      const afterSet = { label: controller.selectedEntry().label, timestamp: controller.selectedEntry().labelTimestamp, status: status.textContent };
      await controller.saveLabel("");
      const afterClear = { label: controller.selectedEntry().label, timestamp: controller.selectedEntry().labelTimestamp, status: status.textContent };
      console.log(JSON.stringify({ requests, afterSet, afterClear, reloads }));
    JS

    assert_equal [
      { "url" => "/sessions/tree/label", "method" => "POST", "body" => { "session" => "/session", "entry_id" => "entry-1", "label" => "checkpoint" } },
      { "url" => "/sessions/tree/label", "method" => "POST", "body" => { "session" => "/session", "entry_id" => "entry-1", "label" => "" } }
    ], result.fetch("requests")
    assert_equal "checkpoint", result.dig("afterSet", "label")
    assert_nil result.dig("afterSet", "timestamp")
    assert_equal "Label updated.", result.dig("afterSet", "status")
    assert_nil result.dig("afterClear", "label")
    assert_nil result.dig("afterClear", "timestamp")
    assert_equal "Label cleared.", result.dig("afterClear", "status")
    assert_equal 2, result.fetch("reloads")
  end

  def test_ctrl_o_cycles_to_the_next_filter
    result = run_javascript(<<~JS)
      const { TreeSessionController } = await import(#{ASSET_URL.to_json});
      const filter = { value: "default" };
      const modal = { hidden: false, querySelector: (selector) => selector === "[data-tree-filter]" ? filter : null };
      const document = { addEventListener() {}, querySelector: () => modal };
      const controller = new TreeSessionController(document, {});
      let changes = 0;
      controller.applyFilterChoice = () => { changes += 1; };
      let prevented = false;
      controller.handleKeydown({
        key: "o", ctrlKey: true, metaKey: false, altKey: false, shiftKey: false,
        preventDefault() { prevented = true; },
        target: { closest() { return null; } }
      });
      console.log(JSON.stringify({ value: filter.value, changes, prevented }));
    JS

    assert_equal "no-tools", result.fetch("value")
    assert_equal 1, result.fetch("changes")
    assert result.fetch("prevented")
  end

  def test_tree_navigation_keys_do_not_override_modal_buttons
    result = run_javascript(<<~JS)
      const { TreeSessionController, TreeSessionModel } = await import(#{ASSET_URL.to_json});
      const modal = { hidden: false, querySelector: () => null };
      const document = { addEventListener() {}, querySelector: () => modal };
      const controller = new TreeSessionController(document, {});
      controller.model = new TreeSessionModel([{ entryId: "entry-1", current: true }]);
      let navigations = 0;
      controller.requestNavigation = () => { navigations += 1; };
      controller.handleKeydown({
        key: "Enter", ctrlKey: false, metaKey: false, altKey: false, shiftKey: false,
        preventDefault() {},
        target: { closest() { return null; } }
      });
      console.log(JSON.stringify({ navigations }));
    JS

    assert_equal 0, result.fetch("navigations")
  end

  def test_filter_and_summary_choices_are_complete_and_exact
    result = run_javascript(<<~JS)
      const { TREE_FILTERS, TREE_SUMMARY_CHOICES } = await import(#{ASSET_URL.to_json});
      console.log(JSON.stringify({ filters: TREE_FILTERS, summaries: TREE_SUMMARY_CHOICES }));
    JS

    assert_equal %w[default no-tools user-only labeled-only all], result.fetch("filters").map { |choice| choice.fetch("value") }
    assert_equal ["No summary", "Summarize", "Summarize with custom instructions"], result.fetch("summaries").map { |choice| choice.fetch("label") }
  end

  def test_modal_exposes_tree_controls_label_actions_and_two_navigation_steps
    view = File.read(VIEW_PATH)

    %w[data-tree-search data-tree-filter data-tree-label-timestamps data-tree-viewport data-tree-label-input data-tree-label-save data-tree-label-clear data-tree-navigate data-tree-summary-step data-tree-summary-submit data-tree-help].each do |attribute|
      assert_includes view, attribute
    end
    assert_includes view, "/sessions/tree/label"
    assert_includes view, "Summarize with custom instructions"
  end

  def test_app_only_prefills_an_empty_composer_after_navigation
    app = File.read(APP_PATH)

    assert_includes app, "if (promptTextarea && !promptTextarea.value && payload?.editorText !== undefined)"
    assert_includes app, "promptTextarea.value = payload.editorText;"
    assert_includes app, "await refreshCurrentSessionPreservingComposer();"
  end

  private

  def run_javascript(source)
    stdout, stderr, status = Open3.capture3("node", "--input-type=module", "-e", source)
    assert status.success?, stderr
    JSON.parse(stdout)
  end
end
