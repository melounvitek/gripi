require "minitest/autorun"
require "json"
require "open3"
require "nokogiri"

class TreeSessionControllerJsTest < Minitest::Test
  ASSET_URL = "file://#{File.expand_path("../public/assets/tree_session_controller.js", __dir__)}"
  VIEW_PATH = File.expand_path("../views/_fork_session_modal.erb", __dir__)
  APP_PATH = File.expand_path("../public/assets/app.js", __dir__)
  CSS_PATH = File.expand_path("../public/assets/app.css", __dir__)

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

  def test_visual_indentation_only_grows_around_visible_forks
    result = run_javascript(<<~JS)
      const { TreeSessionModel } = await import(#{ASSET_URL.to_json});
      const model = new TreeSessionModel([
        { entryId: "root", parentId: null, text: "Root match" },
        { entryId: "branch-a", parentId: "root", text: "Branch A match" },
        { entryId: "a-child", parentId: "branch-a", text: "A child match" },
        { entryId: "a-grandchild", parentId: "a-child", text: "A grandchild match" },
        { entryId: "branch-b", parentId: "root", text: "Branch B" }
      ]);
      const before = Object.fromEntries([...model.visibleStructure().visual].map(([id, value]) => [id, value.indent]));
      model.setSearch("match");
      const after = Object.fromEntries([...model.visibleStructure().visual].map(([id, value]) => [id, value.indent]));
      console.log(JSON.stringify({ before, after }));
    JS

    assert_equal({ "root" => 0, "branch-a" => 1, "a-child" => 2, "a-grandchild" => 2, "branch-b" => 1 }, result.fetch("before"))
    assert_equal({ "root" => 0, "branch-a" => 0, "a-child" => 0, "a-grandchild" => 0 }, result.fetch("after"))
  end

  def test_multiple_visible_roots_share_pi_cli_virtual_fork_indentation
    result = run_javascript(<<~JS)
      const { TreeSessionModel } = await import(#{ASSET_URL.to_json});
      const model = new TreeSessionModel([
        { entryId: "first-root", parentId: null },
        { entryId: "first-child", parentId: "first-root" },
        { entryId: "first-grandchild", parentId: "first-child" },
        { entryId: "second-root", parentId: null }
      ]);
      const visual = model.visibleStructure().visual;
      console.log(JSON.stringify(Object.fromEntries([...visual].map(([id, value]) => [id, value.indent]))));
    JS

    assert_equal({ "first-root" => 0, "first-child" => 1, "first-grandchild" => 1, "second-root" => 0 }, result)
  end

  def test_visual_structure_keeps_connectors_for_the_nearest_visible_fork
    result = run_javascript(<<~JS)
      const { TreeSessionModel } = await import(#{ASSET_URL.to_json});
      const model = new TreeSessionModel([
        { entryId: "root", parentId: null },
        { entryId: "first", parentId: "root" },
        { entryId: "first-child", parentId: "first" },
        { entryId: "second", parentId: "root" }
      ]);
      const visual = model.visibleStructure().visual;
      console.log(JSON.stringify(Object.fromEntries([...visual].map(([id, value]) => [id, value]))));
    JS

    assert_equal true, result.dig("first", "showConnector")
    assert_equal false, result.dig("first", "isLast")
    assert_equal [{ "position" => 0, "show" => true }], result.dig("first-child", "gutters")
    assert_equal true, result.dig("second", "isLast")
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

  def test_controller_only_shows_current_and_distinct_latest_badges
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
      controller.model = new TreeSessionModel([
        { entryId: "current", parentId: null, text: "Current entry", current: true, latest: true },
        { entryId: "latest", parentId: null, text: "Latest entry", latest: true }
      ]);
      controller.render();
      const text = (node) => [node.textContent, ...node.children.flatMap((child) => text(child))];
      console.log(JSON.stringify(viewport.children.map((item) => text(item).filter(Boolean))));
    JS

    assert_includes result[0], "Current"
    refute_includes result[0], "Latest"
    refute result.flatten.include?("Active")
    assert_includes result[1], "Latest"
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

  def test_opening_starts_with_closed_inactive_options
    result = run_javascript(<<~JS)
      const { TreeSessionController, TreeSessionModel } = await import(#{ASSET_URL.to_json});
      const options = { open: true };
      const search = { value: "stale query" };
      const labelTimestamps = { checked: true };
      const browser = { hidden: true };
      const summary = { hidden: false };
      const controls = {
        "[data-tree-options]": options,
        "[data-tree-search]": search,
        "[data-tree-label-timestamps]": labelTimestamps,
        "[data-tree-browser-step]": browser,
        "[data-tree-summary-step]": summary
      };
      const modal = { querySelector: (selector) => controls[selector] || null };
      const document = { addEventListener() {}, querySelector: () => modal };
      const controller = new TreeSessionController(document, {}, { openModal() {} });
      controller.model = new TreeSessionModel([{ entryId: "visible", text: "Visible" }, { entryId: "hidden", text: "Hidden" }]);
      controller.model.setSearch("visible");
      let renders = 0;
      controller.render = () => { renders += 1; };
      controller.open();
      console.log(JSON.stringify({ optionsOpen: options.open, search: search.value, modelQuery: controller.model.query, labelTimestamps: labelTimestamps.checked, browserHidden: browser.hidden, summaryHidden: summary.hidden, renders }));
    JS

    assert_equal false, result.fetch("optionsOpen")
    assert_equal "", result.fetch("search")
    assert_equal "", result.fetch("modelQuery")
    assert_equal 1, result.fetch("renders")
    assert_equal false, result.fetch("labelTimestamps")
    assert_equal false, result.fetch("browserHidden")
    assert_equal true, result.fetch("summaryHidden")
  end

  def test_search_and_label_shortcuts_reveal_options_before_focusing
    result = run_javascript(<<~JS)
      const { TreeSessionController } = await import(#{ASSET_URL.to_json});
      const details = { open: false };
      const focusOrder = [];
      const control = (name) => ({ focus() { focusOrder.push(`${name}:${details.open}`); } });
      const controls = { "[data-tree-search]": control("search"), "[data-tree-label-input]": control("label"), "[data-tree-options]": details };
      const modal = { hidden: false, querySelector: (selector) => controls[selector] || null };
      const document = { addEventListener() {}, querySelector: () => modal };
      const controller = new TreeSessionController(document, {});
      const event = (key, extras = {}) => ({
        key, ctrlKey: false, metaKey: false, altKey: false, shiftKey: false,
        preventDefault() {}, target: { closest() { return null; } }, ...extras
      });
      controller.handleKeydown(event("/"));
      details.open = false;
      controller.handleKeydown(event("L", { shiftKey: true }));
      console.log(JSON.stringify(focusOrder));
    JS

    assert_equal ["search:true", "label:true"], result
  end

  def test_ctrl_o_reveals_options_and_cycles_to_the_next_filter
    result = run_javascript(<<~JS)
      const { TreeSessionController } = await import(#{ASSET_URL.to_json});
      const filter = { value: "default" };
      const details = { open: false };
      const modal = { hidden: false, querySelector: (selector) => ({ "[data-tree-filter]": filter, "[data-tree-options]": details })[selector] || null };
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
      console.log(JSON.stringify({ value: filter.value, changes, prevented, optionsOpen: details.open }));
    JS

    assert_equal "no-tools", result.fetch("value")
    assert_equal 1, result.fetch("changes")
    assert result.fetch("prevented")
    assert result.fetch("optionsOpen")
  end

  def test_tree_navigation_keys_do_not_override_native_disclosure_controls
    result = run_javascript(<<~JS)
      const { TreeSessionController, TreeSessionModel } = await import(#{ASSET_URL.to_json});
      const modal = { hidden: false, querySelector: () => null };
      const document = { addEventListener() {}, querySelector: () => modal };
      const controller = new TreeSessionController(document, {});
      controller.model = new TreeSessionModel([{ entryId: "entry-1", current: true }]);
      let navigations = 0;
      let prevented = false;
      controller.requestNavigation = () => { navigations += 1; };
      controller.handleKeydown({
        key: "Enter", ctrlKey: false, metaKey: false, altKey: false, shiftKey: false,
        preventDefault() { prevented = true; },
        target: { closest(selector) { return selector === "summary" ? this : null; } }
      });
      console.log(JSON.stringify({ navigations, prevented }));
    JS

    assert_equal 0, result.fetch("navigations")
    assert_equal false, result.fetch("prevented")
  end

  def test_filter_and_summary_choices_are_complete_and_exact
    result = run_javascript(<<~JS)
      const { TREE_FILTERS, TREE_SUMMARY_CHOICES } = await import(#{ASSET_URL.to_json});
      console.log(JSON.stringify({ filters: TREE_FILTERS, summaries: TREE_SUMMARY_CHOICES }));
    JS

    assert_equal %w[default no-tools user-only labeled-only all], result.fetch("filters").map { |choice| choice.fetch("value") }
    assert_equal ["No summary", "Summarize", "Summarize with custom instructions"], result.fetch("summaries").map { |choice| choice.fetch("label") }
  end

  def test_modal_keeps_secondary_controls_in_a_closed_options_disclosure
    document = Nokogiri::HTML.fragment(File.read(VIEW_PATH))
    options = document.at_css("details[data-tree-options]")

    refute_nil options
    assert_nil options["open"]
    assert_equal "Search & options", options.at_css("summary").text.strip
    assert document.at_css('[data-modal="tree-session-modal"] [data-modal-default-focus][data-modal-close]')
    %w[data-tree-search data-tree-filter data-tree-label-timestamps data-tree-label-input data-tree-label-save data-tree-label-clear data-tree-help].each do |attribute|
      assert options.at_css("[#{attribute}]")
    end
    %w[data-tree-viewport data-tree-navigate data-tree-summary-step data-tree-summary-submit].each do |attribute|
      assert document.at_css("[#{attribute}]")
    end
    assert_includes document.text, "Summarize with custom instructions"
  end

  def test_tree_modal_css_prioritizes_vertical_tree_scrolling_and_caps_mobile_indentation
    css = File.read(CSS_PATH)

    assert_includes css, "grid-template-rows: minmax(0, auto) auto minmax(0, 1fr) auto"
    assert_includes css, ".tree-session-list { min-width: 0; min-height: 0; overflow-y: auto; overflow-x: hidden;"
    assert_includes css, ".tree-session-connector-level:nth-last-child(n + 9)"
    assert_includes css, ".tree-session-connector-level:nth-last-child(n + 4)"
    assert_includes css, ".tree-session-summary { overflow-y: auto; }"
    assert_includes css, "env(safe-area-inset-bottom)"
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
