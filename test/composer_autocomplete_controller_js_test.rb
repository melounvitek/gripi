require "minitest/autorun"
require "json"
require "open3"

class ComposerAutocompleteControllerJsTest < Minitest::Test
  ASSETS = File.expand_path("../public/assets", __dir__)

  def test_extracts_caret_local_contexts_and_applies_pi_style_completion
    results = run_javascript(<<~JS)
      const { composerPathContext, applyComposerPathCompletion } = await import(#{module_url.to_json});
      const value = "first line\\nopen @src/uti suffix";
      const caret = value.indexOf(" suffix");
      const at = composerPathContext(value, caret);
      const completed = applyComposerPathCompletion(value, caret, at, {
        path: "src/user interface.js", directory: false
      });
      const directory = applyComposerPathCompletion('see @"my d" later', 10, composerPathContext('see @"my d" later', 10), {
        path: "my dir/", directory: true
      });
      const ordinary = applyComposerPathCompletion("run ./scr now", 9, composerPathContext("run ./scr now", 9, { force: true }), {
        path: "./scripts/", directory: true
      });
      console.log(JSON.stringify({
        at, completed, directory, ordinary,
        equals: composerPathContext("file=src/ma", 11, { force: true }),
        invalidAt: composerPathContext("email@example", 13),
        naturalPath: composerPathContext("look src/ma", 11, { natural: true }),
        naturalWord: composerPathContext("look readme", 11, { natural: true }),
        priorLineQuote: composerPathContext('unfinished "quote\\nuse @src', 26)
      }));
    JS

    assert_equal({ "mode" => "fuzzy", "query" => "src/uti", "token" => "@src/uti", "start" => 16, "quoted" => false }, results.fetch("at"))
    assert_equal "first line\nopen @\"src/user interface.js\"  suffix", results.dig("completed", "value")
    assert_equal 41, results.dig("completed", "selectionStart")
    assert_equal 'see @"my dir/" later', results.dig("directory", "value")
    assert_equal 13, results.dig("directory", "selectionStart")
    assert_equal "run ./scripts/ now", results.dig("ordinary", "value")
    assert_equal 14, results.dig("ordinary", "selectionStart")
    assert_equal "src/ma", results.dig("equals", "query")
    assert_nil results.fetch("invalidAt")
    assert_equal "path", results.dig("naturalPath", "mode")
    assert_nil results.fetch("naturalWord")
    assert_equal "fuzzy", results.dig("priorLineQuote", "mode")
    assert_equal "src", results.dig("priorLineQuote", "query")
  end

  def test_debounces_requests_ignores_composition_aborts_stale_work_and_rebinds
    results = run_javascript(<<~JS)
      const { ComposerAutocompleteController } = await import(#{module_url.to_json});
      #{fake_dom}
      const document = new FakeDocument();
      const timers = new Map();
      let timerId = 0;
      globalThis.setTimeout = (callback, delay) => { const id = ++timerId; timers.set(id, { callback, delay }); return id; };
      globalThis.clearTimeout = (id) => timers.delete(id);
      const requests = [];
      globalThis.fetch = (url, options) => new Promise((resolve) => requests.push({ url, options, resolve }));
      const textarea = new FakeElement("textarea");
      const list = new FakeElement("div"); list.id = "paths";
      const controller = new ComposerAutocompleteController(document, { currentSessionPath: () => "session-a", debounceMs: 75 });
      controller.bind(textarea, list);
      textarea.value = "use @src"; textarea.selectionStart = textarea.value.length;
      textarea.dispatch("input");
      const scheduled = [...timers.values()][0];
      scheduled.callback();
      textarea.value = "use @spec"; textarea.selectionStart = textarea.value.length;
      controller.load({ mode: "fuzzy", query: "spec", token: "@spec", start: 4, quoted: false });
      const firstAborted = requests[0].options.signal.aborted;
      textarea.dispatch("compositionstart");
      textarea.value = "use @ignored"; textarea.dispatch("input");
      const requestsDuringComposition = requests.length;
      const oldTextarea = textarea;
      const replacement = new FakeElement("textarea");
      const replacementList = new FakeElement("div"); replacementList.id = "replacement-paths";
      controller.bind(replacement, replacementList);
      replacement.value = "use @new"; replacement.selectionStart = replacement.value.length;
      replacement.dispatch("input");
      const reboundScheduled = timers.size;
      requests[0].resolve({ ok: true, json: async () => ({ suggestions: [{ path: "stale", directory: false }] }) });
      await Promise.resolve(); await Promise.resolve();
      console.log(JSON.stringify({
        delay: scheduled.delay,
        firstBody: Object.fromEntries(requests[0].options.body),
        firstAborted,
        requestsDuringComposition,
        oldInputListeners: oldTextarea.listenerCount("input"),
        newInputListeners: replacement.listenerCount("input"),
        reboundScheduled,
        oldListHidden: list.hidden,
        replacementListChildren: replacementList.children.length,
        aria: [replacement.getAttribute("role"), replacement.getAttribute("aria-controls"), replacement.getAttribute("aria-expanded")]
      }));
    JS

    assert_equal 75, results.fetch("delay")
    assert_equal({ "session" => "session-a", "mode" => "fuzzy", "query" => "src" }, results.fetch("firstBody"))
    assert_equal true, results.fetch("firstAborted")
    assert_equal 2, results.fetch("requestsDuringComposition")
    assert_equal 0, results.fetch("oldInputListeners")
    assert_equal 1, results.fetch("newInputListeners")
    assert_equal 1, results.fetch("reboundScheduled")
    assert_equal true, results.fetch("oldListHidden")
    assert_equal 0, results.fetch("replacementListChildren")
    assert_equal ["combobox", "replacement-paths", "false"], results.fetch("aria")
  end

  def test_keyboard_and_pointer_selection_preserve_unhandled_shortcuts
    results = run_javascript(<<~JS)
      const { ComposerAutocompleteController } = await import(#{module_url.to_json});
      #{fake_dom}
      const document = new FakeDocument();
      const textarea = new FakeElement("textarea");
      const list = new FakeElement("div"); list.id = "paths";
      const requests = [];
      globalThis.fetch = async (_url, options) => {
        requests.push(Object.fromEntries(options.body));
        return { ok: true, json: async () => ({ suggestions: [
          { path: "src/", directory: true },
          { path: "src/app.js", directory: false }
        ] }) };
      };
      const controller = new ComposerAutocompleteController(document, { currentSessionPath: () => "s" });
      controller.bind(textarea, list);
      textarea.value = "edit @sr tail"; textarea.selectionStart = 8;
      await controller.load({ mode: "fuzzy", query: "sr", token: "@sr", start: 5, quoted: false });
      textarea.selectionStart = 7;
      const staleEnter = key("Enter");
      const handledStaleEnter = controller.handleKeydown(staleEnter);
      const staleValue = textarea.value;
      textarea.selectionStart = 8;
      await controller.load({ mode: "fuzzy", query: "sr", token: "@sr", start: 5, quoted: false });
      const escape = key("Escape");
      const handledEscape = controller.handleKeydown(escape);
      const expandedAfterEscape = textarea.getAttribute("aria-expanded");
      await controller.load({ mode: "fuzzy", query: "sr", token: "@sr", start: 5, quoted: false });
      const down = key("ArrowDown");
      const handledDown = controller.handleKeydown(down);
      const enter = key("Enter");
      const handledEnter = controller.handleKeydown(enter);
      const accepted = { value: textarea.value, caret: textarea.selectionStart };
      textarea.value = "open ./sc end"; textarea.selectionStart = 9; textarea.selectionEnd = 9;
      const tab = key("Tab");
      const handledTab = controller.handleKeydown(tab);
      await Promise.resolve(); await Promise.resolve();
      const shiftTab = key("Tab", { shiftKey: true });
      const altEnter = key("Enter", { altKey: true });
      const pointerPrevented = { value: false };
      const pointerOption = list.children[0];
      list.dispatch("pointerdown", { target: pointerOption, pointerType: "touch", preventDefault() { pointerPrevented.value = true; } });
      list.dispatch("click", { target: pointerOption, preventDefault() {} });
      console.log(JSON.stringify({
        handledStaleEnter, staleValue, handledEscape, escapePrevented: escape.prevented, expandedAfterEscape,
        handledDown, handledEnter, accepted, handledTab, tabPrevented: tab.prevented,
        shiftTab: controller.handleKeydown(shiftTab), altEnter: controller.handleKeydown(altEnter),
        requests, pointerPrevented: pointerPrevented.value, finalValue: textarea.value,
        expanded: textarea.getAttribute("aria-expanded")
      }));
      function key(key, extra = {}) { return { key, shiftKey: false, ctrlKey: false, metaKey: false, altKey: false, isComposing: false, prevented: false, preventDefault() { this.prevented = true; }, ...extra }; }
    JS

    assert_equal true, results.fetch("handledStaleEnter")
    assert_equal "edit @sr tail", results.fetch("staleValue")
    assert_equal true, results.fetch("handledEscape")
    assert_equal true, results.fetch("escapePrevented")
    assert_equal "false", results.fetch("expandedAfterEscape")
    assert_equal true, results.fetch("handledDown")
    assert_equal true, results.fetch("handledEnter")
    assert_equal "edit @src/app.js  tail", results.dig("accepted", "value")
    assert_equal 17, results.dig("accepted", "caret")
    assert_equal true, results.fetch("handledTab")
    assert_equal true, results.fetch("tabPrevented")
    assert_equal false, results.fetch("shiftTab")
    assert_equal false, results.fetch("altEnter")
    assert_equal "path", results.dig("requests", 3, "mode")
    assert_equal true, results.fetch("pointerPrevented")
    assert_equal "open src/ end", results.fetch("finalValue")
    assert_equal "false", results.fetch("expanded")
  end

  private

  def module_url
    "file://#{File.join(ASSETS, "composer_autocomplete_controller.js")}"
  end

  def run_javascript(source)
    stdout, stderr, status = Open3.capture3("node", "--input-type=module", "-e", source)
    assert status.success?, stderr
    JSON.parse(stdout)
  end

  def fake_dom
    <<~JS
      class FakeElement {
        constructor(tag = "div") { this.tagName = tag; this.listeners = new Map(); this.attributes = new Map(); this.dataset = {}; this.children = []; this.parentElement = null; this.hidden = false; this.value = ""; this.selectionStart = 0; this.textContent = ""; }
        addEventListener(type, listener) { const values = this.listeners.get(type) || []; values.push(listener); this.listeners.set(type, values); }
        removeEventListener(type, listener) { this.listeners.set(type, (this.listeners.get(type) || []).filter((value) => value !== listener)); }
        listenerCount(type) { return (this.listeners.get(type) || []).length; }
        dispatch(type, event = {}) { event.target ||= this; (this.listeners.get(type) || []).forEach((listener) => listener(event)); }
        dispatchEvent(event) { (this.listeners.get(event.type) || []).forEach((listener) => listener(event)); return true; }
        setAttribute(name, value) { this.attributes.set(name, String(value)); }
        getAttribute(name) { return this.attributes.get(name) ?? null; }
        removeAttribute(name) { this.attributes.delete(name); }
        append(child) { child.parentElement = this; this.children.push(child); }
        replaceChildren() { this.children.forEach((child) => child.parentElement = null); this.children = []; }
        contains(element) { return element === this || this.children.includes(element); }
        closest(selector) { return selector === "[data-composer-path-option]" && this.dataset.composerPathOption !== undefined ? this : null; }
        setSelectionRange(start, end = start) { this.selectionStart = start; this.selectionEnd = end; }
        focus() {}
        scrollIntoView() {}
      }
      class FakeDocument { createElement(tag) { return new FakeElement(tag); } }
    JS
  end
end
