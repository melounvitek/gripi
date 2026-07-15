require "minitest/autorun"
require "json"
require "open3"
require "nokogiri"

class DemoTest < Minitest::Test
  ROOT = File.expand_path("..", __dir__)
  HTML = File.join(ROOT, "demo/index.html")
  JAVASCRIPT = File.join(ROOT, "demo/demo.js")
  PRODUCTION_CSS = File.join(ROOT, "public/assets/app.css")
  DEPLOY_WORKFLOW = File.join(ROOT, ".github/workflows/deploy_demo.yml")

  def test_demo_deploys_to_github_pages_after_every_master_commit
    assert_path_exists DEPLOY_WORKFLOW
    workflow = File.read(DEPLOY_WORKFLOW)

    assert_match(/push:\n\s+branches: \[master\]/, workflow)
    assert_includes workflow, "workflow_dispatch:"
    assert_includes workflow, "pages: write"
    assert_includes workflow, "id-token: write"
    assert_includes workflow, "uses: actions/configure-pages@v5"
    refute_includes workflow, "enablement: true"
    assert_includes workflow, "uses: actions/upload-pages-artifact@v4"
    assert_includes workflow, "path: ./demo"
    assert_includes workflow, "uses: actions/deploy-pages@v4"
  end

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
      .conversation-scroll>#history-output
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
    refute body.at_css(".current-session-section, .session-relation-tree")
    refute body.at_css(".session.unread, .mobile-sessions-unread-badge")
    refute body.at_css(".jump-controls.is-visible, .jump-button.is-visible")
    assert_equal "pi", body.at_css(".message--assistant.message--thinking .role").text
    assert_equal "pi", body.at_css(".message--assistant:not(.message--thinking):not(.message--tool-call) .role").text
  end

  def test_intro_session_is_open_by_default_with_installation_and_repository_link
    body = Nokogiri::HTML5(File.read(HTML)).at_css("body")
    repository_link = body.at_css('#history-output a[href="https://github.com/melounvitek/gripi"]')
    result = run_javascript("console.log(JSON.stringify({ defaultSessionId: GripiDemo.defaultSessionId }));")

    assert_equal "Welcome to GRIPi · GRIPi demo", body.document.at_css("title").text
    assert_equal "Welcome to GRIPi", body.at_css(".session-header-name").text
    assert_equal "welcome", result.fetch("defaultSessionId")
    assert_includes body.at_css("#history-output").text, "mise run setup"
    refute_nil repository_link
    refute repository_link.attribute("target")
    assert_match(/gripi:static-demo:v\d+/, File.read(JAVASCRIPT))
  end

  def test_demo_replaces_gripi_dummies_with_the_guide_catalogue
    result = run_javascript("console.log(JSON.stringify(GripiDemo.sessionCatalog));")
    grouped = result.group_by { |session| session.fetch("project") }

    assert_equal [
      "Welcome to GRIPi",
      "New to Pi? Start here",
      "What isn’t supported in GRIPi?",
      "Run GRIPi on an always-on computer",
      "Access GRIPi remotely with Tailscale",
      "Use GRIPi from a phone or tablet",
      "Should I run GRIPi on a VPS?"
    ], grouped.fetch("gripi").map { |session| session.fetch("name") }
    assert_equal ["Draft release notes", "Simplify documentation navigation"], grouped.fetch("website").map { |session| session.fetch("name") }
    assert_equal ["Investigate flaky checkout spec", "Polish checkout confirmation copy", "Speed up CI dependency caching"], grouped.fetch("storefront").map { |session| session.fetch("name") }
    assert_equal ["Welcome to GRIPi"], grouped.fetch("gripi").select { |session| session.fetch("pinned") }.map { |session| session.fetch("name") }

    body = Nokogiri::HTML5(File.read(HTML)).at_css("body")
    javascript = File.read(JAVASCRIPT)
    refute_includes javascript, 'gripi:static-demo:v5'
    assert_includes javascript, 'gripi:static-demo:v6'
    assert_includes javascript, "Custom TUI components, overlays, widgets, editors"
    assert_includes javascript, "Never expose GRIPi through a public IP or public reverse proxy."
    assert_includes javascript, 'switchSession(button.dataset.demoTreeTarget)'
    assert body.at_css('[data-demo-tree-target="new-to-pi"]')

    source = javascript + File.read(HTML)
    ["Build responsive session sidebar", "Improve gateway error handling", "Fix streamed markdown rendering", "Add session keyboard shortcuts"].each do |removed_title|
      refute_includes source, removed_title
    end
  end

  def test_guide_links_are_restricted_to_trusted_destinations
    result = run_javascript(<<~JS)
      console.log(JSON.stringify({
        pi: GripiDemo.safeGuideLink({ href: "https://pi.dev/", label: "Pi" }),
        docs: GripiDemo.safeGuideLink({ href: "https://github.com/melounvitek/gripi/blob/master/docs/examples.md", label: "Guide" }),
        unsafe: GripiDemo.safeGuideLink({ href: "javascript:alert(1)", label: "Unsafe" })
      }));
    JS

    assert_equal({ "href" => "https://pi.dev/", "label" => "Pi" }, result.fetch("pi"))
    assert_equal "https://github.com/melounvitek/gripi/blob/master/docs/examples.md", result.fetch("docs").fetch("href")
    assert_nil result.fetch("unsafe")
  end

  def test_demo_notice_links_to_the_repository_in_the_same_tab
    body = Nokogiri::HTML5(File.read(HTML)).at_css("body")
    link = body.at_css('#demo-notice a[href="https://github.com/melounvitek/gripi"]')

    refute_nil link
    assert_includes link.ancestors("#demo-notice").first["class"].split, "is-visible"
    assert link.parent.at_css("[data-demo-notice-message]")
    assert_equal "View GRIPi on GitHub →", link.text
    refute link.attribute("target")
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
