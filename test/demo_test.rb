require "minitest/autorun"
require "json"
require "open3"
require "nokogiri"

class DemoTest < Minitest::Test
  ROOT = File.expand_path("..", __dir__)
  HTML = File.join(ROOT, "demo/index.html")
  JAVASCRIPT = File.join(ROOT, "demo/demo.js")
  PRODUCTION_CSS = File.join(ROOT, "public/assets/app.css")
  def test_demo_is_two_self_contained_portable_files
    assert_path_exists HTML
    assert_path_exists JAVASCRIPT
    assert_equal %w[demo.js index.html], Dir.children(File.join(ROOT, "demo")).sort

    html = File.read(HTML)
    javascript = File.read(JAVASCRIPT)

    assert_includes html, "<style>"
    assert_includes html, '<script src="demo.js"></script>'
    refute_match(/<(?:link|script|img|iframe|source|object|embed)[^>]+(?:href|src|data)=["'](?:https?:|\/)/i, html)
    refute_match(/url\s*\(\s*["']?(?:https?:|\/)/i, html)
    refute_match(/@import\b/i, html)
    refute_match(/\b(?:fetch|XMLHttpRequest|EventSource|WebSocket|sendBeacon)\b/, javascript)
    refute_match(/^\s*(?:import|export)\s/m, javascript)
  end

  def test_demo_embeds_the_exact_production_stylesheet
    html = File.read(HTML)
    embedded_css = html.match(/<style data-production-styles>\n(.*?)<\/style>/m)&.[](1)

    refute_nil embedded_css
    assert_equal File.read(PRODUCTION_CSS), embedded_css
  end

  def test_demo_uses_production_ui_structure
    body = Nokogiri::HTML5(File.read(HTML)).at_css("body")

    %w[
      .app-shell>.session-sidebar .session-sidebar-content_.recent-sessions
      .sidebar-project-filter .sessions-list .conversation-panel>.session-header
      .session-header-title>.session-relation-tree .conversation-scroll>#history-output
      .current-session-find .jump-controls .message.message--user
      .message.message--assistant.message--thinking .message.message--assistant.message--tool-call
      .composer>.composer-inner .command-list .prompt-form_.attachment-tray
      .session-status-bar_.model-settings-chip[data-status-key=model]
      [data-modal=new-session-modal] [data-modal=fork-session-modal]
      [data-modal=tree-session-modal] [data-modal=model-settings-modal]
      .sidebar-notification-toggle .session-switch-overlay
    ].each do |encoded_selector|
      selector = encoded_selector.tr("_", " ")
      assert body.at_css(selector), "Expected body to include #{selector}"
    end
    refute body.at_css(".current-session-section")
    refute body.at_css(".session.unread, .mobile-sessions-unread-badge")
    refute body.at_css(".jump-controls.is-visible, .jump-button.is-visible")
    assert_equal "pi", body.at_css(".message--assistant.message--thinking .role").text
    assert_equal "pi", body.at_css(".message--assistant:not(.message--thinking):not(.message--tool-call) .role").text
  end

  def test_streams_are_bound_to_the_originating_session_and_blocked_while_switching
    javascript = File.read(JAVASCRIPT)

    assert_includes javascript, "if (!prompt || streamController || switching) return;"
    assert_includes javascript, "appendStreamEvent(event, streamSession)"
    assert_includes javascript, "if (generation !== switchGeneration) return;"
  end

  def test_jump_arrows_only_appear_for_the_current_scroll_direction
    result = run_javascript(<<~JS)
      console.log(JSON.stringify({
        up: GripiDemo.jumpControlVisibility(500, 300, 900),
        down: GripiDemo.jumpControlVisibility(300, 500, 900),
        top: GripiDemo.jumpControlVisibility(200, 50, 900),
        bottom: GripiDemo.jumpControlVisibility(700, 850, 900)
      }));
    JS

    assert_equal({ "top" => true, "bottom" => false }, result.fetch("up"))
    assert_equal({ "top" => false, "bottom" => true }, result.fetch("down"))
    assert_equal({ "top" => false, "bottom" => false }, result.fetch("top"))
    assert_equal({ "top" => false, "bottom" => false }, result.fetch("bottom"))
  end

  def test_demo_includes_a_full_session_list_without_unread_state
    result = run_javascript(<<~JS)
      console.log(JSON.stringify({ count: GripiDemo.demoSessionCount, unread: GripiDemo.hasUnreadSessions }));
    JS

    assert_operator result.fetch("count"), :>=, 8
    assert_equal false, result.fetch("unread")
  end

  def test_persisted_identity_colors_are_restricted_to_hex_values
    result = run_javascript(<<~JS)
      console.log(JSON.stringify({
        valid: GripiDemo.safeIdentityColor("#12abEF", "#000000"),
        invalid: GripiDemo.safeIdentityColor("red;background:url(//example.test)", "#123456")
      }));
    JS

    assert_equal "#12abEF", result.fetch("valid")
    assert_equal "#123456", result.fetch("invalid")
  end

  def test_scripted_response_can_finish_and_be_cancelled
    result = run_javascript(<<~JS)
      const events = [];
      await GripiDemo.playScript(
        [{ type: "status", text: "Thinking" }, { type: "delta", text: "Hello" }, { type: "done" }],
        { wait: async () => {}, onEvent: event => events.push(event.type) }
      );

      const controller = new AbortController();
      const cancelled = [];
      await GripiDemo.playScript(
        [{ type: "delta", text: "A" }, { type: "delta", text: "B" }],
        {
          signal: controller.signal,
          wait: async () => controller.abort(),
          onEvent: event => cancelled.push(event.text)
        }
      );

      console.log(JSON.stringify({ events, cancelled }));
    JS

    assert_equal %w[status delta done], result.fetch("events")
    assert_equal [], result.fetch("cancelled")
  end

  def test_response_script_contains_visible_streaming_stages
    result = run_javascript(<<~JS)
      const script = GripiDemo.responseScript("How does this work?");
      console.log(JSON.stringify({
        types: [...new Set(script.map(event => event.type))],
        answer: script.filter(event => event.type === "delta").map(event => event.text).join("")
      }));
    JS

    assert_equal %w[status thinking tool_start tool_end assistant_start delta done], result.fetch("types")
    assert_includes result.fetch("answer"), "How does this work?"
  end

  private

  def run_javascript(source)
    script = File.read(JAVASCRIPT)
    stdout, stderr, status = Open3.capture3("node", "--input-type=module", "-e", <<~JS)
      globalThis.window = undefined;
      #{script}
      #{source}
    JS
    assert status.success?, stderr
    JSON.parse(stdout)
  end
end
