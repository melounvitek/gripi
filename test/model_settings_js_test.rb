require "minitest/autorun"
require "json"
require "open3"

class ModelSettingsJsTest < Minitest::Test
  VIEW_PATH = File.expand_path("../views/index.erb", __dir__)

  def test_supported_thinking_levels_follow_model_capabilities
    results = run_javascript(<<~JS)
      const THINKING_LEVELS = ["off", "minimal", "low", "medium", "high", "xhigh", "max"];
      #{javascript_function("supportedThinkingLevels")}
      console.log(JSON.stringify([
        supportedThinkingLevels({ reasoning: false, thinkingLevelMap: { off: null } }),
        supportedThinkingLevels({ reasoning: true }),
        supportedThinkingLevels({ reasoning: true, thinkingLevelMap: { off: null, minimal: null, xhigh: "xhigh", max: null } }),
        supportedThinkingLevels({ reasoning: true, thinkingLevelMap: { low: null, max: "max" } })
      ]));
    JS

    assert_equal [
      ["off"],
      ["off", "minimal", "low", "medium", "high"],
      ["low", "medium", "high", "xhigh"],
      ["off", "minimal", "medium", "high", "max"]
    ], results
  end

  def test_unsupported_current_thinking_level_gets_a_visual_fallback
    results = run_javascript(<<~JS)
      const THINKING_LEVELS = ["off", "minimal", "low", "medium", "high", "xhigh", "max"];
      #{javascript_function("supportedThinkingLevels")}
      #{javascript_function("selectedThinkingLevel")}
      console.log(JSON.stringify([
        selectedThinkingLevel({ reasoning: true }, "max"),
        selectedThinkingLevel({ reasoning: true, thinkingLevelMap: { minimal: null } }, "minimal"),
        selectedThinkingLevel({ reasoning: true, thinkingLevelMap: { off: null, minimal: null } }, "off"),
        selectedThinkingLevel({ reasoning: false }, "high")
      ]));
    JS

    assert_equal ["high", "low", "low", "off"], results
  end

  def test_shift_tab_cycle_requires_focused_idle_composer_without_other_modifiers_or_modal
    results = run_javascript(<<~JS)
      let promptTextarea = {};
      let composerState = { dataset: { state: "idle" } };
      let document = { activeElement: promptTextarea };
      let modalIsOpen = () => false;
      #{javascript_function("cycleThinkingShortcut")}
      const event = (extra = {}) => ({ key: "Tab", shiftKey: true, ctrlKey: false, metaKey: false, altKey: false, ...extra });
      const values = [
        cycleThinkingShortcut(event()),
        cycleThinkingShortcut(event({ ctrlKey: true })),
        cycleThinkingShortcut(event({ key: "Enter" })),
        (() => { composerState.dataset.state = "running"; return cycleThinkingShortcut(event()); })(),
        (() => { composerState.dataset.state = "idle"; modalIsOpen = () => true; return cycleThinkingShortcut(event()); })(),
        (() => { modalIsOpen = () => false; document.activeElement = {}; return cycleThinkingShortcut(event()); })()
      ];
      console.log(JSON.stringify(values));
    JS

    assert_equal [true, false, false, false, false, false], results
  end

  private

  def javascript_function(name)
    File.read(VIEW_PATH).match(/^    function #{Regexp.escape(name)}\b.*?^    }$/m)&.[](0) || flunk("Missing JavaScript function #{name}")
  end

  def run_javascript(source)
    stdout, stderr, status = Open3.capture3("node", "-e", source)
    assert status.success?, stderr
    JSON.parse(stdout)
  end
end
