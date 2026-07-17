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
    assert_includes html, '<link rel="icon" type="image/svg+xml" href="data:image/svg+xml,'
    assert_includes html, '<script src="demo.js"></script>'
    refute_includes html, "architecture.png"
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
      .message.message--assistant.message--thinking .message.message--assistant:not(.message--thinking)
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

    assert_equal "Welcome to Gripi · Gripi demo", body.document.at_css("title").text
    assert_equal "Welcome to Gripi", body.at_css(".session-header-name").text
    assert_equal "welcome", result.fetch("defaultSessionId")
    assert_includes body.at_css("#history-output").text, "mise run setup"
    assert_includes body.at_css("#history-output").text, "Pi stays Pi"
    refute_includes body.at_css("#history-output").text, "Installation requirements"
    refute_includes File.read(JAVASCRIPT), 'title: "Installation requirements"'
    refute_nil repository_link
    refute repository_link.attribute("target")
    assert_match(/gripi:static-demo:v\d+/, File.read(JAVASCRIPT))
  end

  def test_demo_replaces_gripi_dummies_with_the_guide_catalogue
    result = run_javascript("console.log(JSON.stringify(GripiDemo.sessionCatalog));")
    grouped = result.group_by { |session| session.fetch("project") }

    assert_equal [
      "Welcome to Gripi",
      "New to Pi? Start here",
      "Does Gripi change Pi?",
      "What isn’t supported in Gripi?",
      "Run Gripi on an always-on computer",
      "Access Gripi remotely with Tailscale",
      "Use Gripi from a phone or tablet",
      "Should I run Gripi on a VPS?",
      "Does this look 1:1 realistic as the real product?"
    ], grouped.fetch("gripi").map { |session| session.fetch("name") }
    assert_equal ["Draft release notes", "Simplify documentation navigation"], grouped.fetch("website").map { |session| session.fetch("name") }
    assert_equal ["Investigate flaky checkout spec", "Polish checkout confirmation copy", "Speed up CI dependency caching"], grouped.fetch("storefront").map { |session| session.fetch("name") }
    assert_equal ["Welcome to Gripi"], result.select { |session| session.fetch("pinned") }.map { |session| session.fetch("name") }

    body = Nokogiri::HTML5(File.read(HTML)).at_css("body")
    javascript = File.read(JAVASCRIPT)
    refute_includes javascript, 'gripi:static-demo:v11'
    assert_includes javascript, 'gripi:static-demo:v12'
    assert_includes javascript, "does not alter Pi’s system prompt"
    assert_includes javascript, "not as polished here as they are in the real app"
    assert_includes javascript, "Custom TUI components, overlays, widgets, editors"
    assert_includes javascript, "Never expose Gripi through a public IP or public reverse proxy."
    assert_includes javascript, 'switchSession(button.dataset.demoTreeTarget)'
    assert body.at_css('[data-demo-tree-target="new-to-pi"]')

    source = javascript + File.read(HTML)
    ["Build responsive session sidebar", "Improve gateway error handling", "Fix streamed markdown rendering", "Add session keyboard shortcuts"].each do |removed_title|
      refute_includes source, removed_title
    end

    assert_includes javascript, 'title: "write content/app/releases.md"'
    assert_includes javascript, 'title: "edit app/views/checkouts/show.html.erb"'
    assert_includes javascript, 'title: "bash bin/rails test test/system/checkout_test.rb"'
    assert_includes javascript, 'title: "read .github/workflows/test.yml"'
  end

  def test_demo_guide_messages_use_current_real_timestamp_format
    javascript = File.read(JAVASCRIPT)
    html = File.read(HTML)
    result = run_javascript(<<~JS)
      const date = new Date(2026, 6, 17, 16, 36);
      console.log(JSON.stringify({ formatted: GripiDemo.formatDemoTimestamp(date) }));
    JS

    refute_includes javascript, 'time: "Welcome"'
    refute_includes javascript, 'time: "Guide"'
    refute_includes html, '<div class="message-meta">Welcome</div>'
    assert_equal "2026-07-17 16:36", result.fetch("formatted")
  end

  def test_demo_sidebar_uses_activity_timestamps_in_session_meta
    result = run_javascript("console.log(JSON.stringify(GripiDemo.sessionCatalog));")
    ages = result.to_h { |session| [session.fetch("name"), session.fetch("age")] }

    assert_equal "just now", ages.fetch("Welcome to Gripi")
    assert_equal "22 minutes ago", ages.fetch("Access Gripi remotely with Tailscale")
    assert_equal "31 minutes ago", ages.fetch("Use Gripi from a phone or tablet")
    assert_equal "1 hour ago", ages.fetch("Does this look 1:1 realistic as the real product?")
    assert_equal "yesterday", ages.fetch("Investigate flaky checkout spec")
    assert_equal "2026-06-17", ages.fetch("Simplify documentation navigation")
  end

  def test_demo_jump_controls_use_delayed_reveal_like_real_gripi
    javascript = File.read(JAVASCRIPT)

    assert_includes javascript, 'let lastRevealAt = 0;'
    assert_includes javascript, 'scrollRevealDelayTimer = setTimeout(() => {'
    assert_includes javascript, 'if (Date.now() - lastRevealAt > 120) return;'
    assert_includes javascript, '}, 300);'
    assert_includes javascript, 'resetJumpControlsReveal();'
    assert_includes javascript, 'if (!visible) return;'
    assert_includes javascript, 'if (!visible.top && !visible.bottom) { resetJumpControlsReveal(); return; }'
    assert_includes javascript, 'latestReadableAssistantMessageIsVisible()'
  end

  def test_demo_jump_controls_do_not_hide_during_programmatic_scroll
    javascript = File.read(JAVASCRIPT)

    assert_includes javascript, 'function programmaticScrollTo(options) {'
    refute_includes javascript, 'programmaticScroll = true;\n    setJumpControls(false, false);\n    element.scroll.scrollTo(options);'
    refute_includes javascript, 'if (programmaticScroll) { lastScrollTop = current; setJumpControls(false, false); finishProgrammaticScrollSoon(); return; }'
  end

  def test_demo_sidebar_preserves_scroll_when_rerendering_sessions
    javascript = File.read(JAVASCRIPT)

    assert_includes javascript, 'sidebarScroll: document.querySelector(".session-sidebar-content")'
    assert_includes javascript, 'const scrollTop = element.sidebarScroll?.scrollTop || 0;'
    assert_includes javascript, 'if (element.sidebarScroll) element.sidebarScroll.scrollTop = scrollTop;'
  end

  def test_demo_project_sessions_show_native_tool_activity
    javascript = File.read(JAVASCRIPT)

    [
      'title: "bash git status --short && git diff --stat origin/main...HEAD"',
      'title: "read app/components/sidebar/search.tsx"',
      'title: "write content/app/releases.md"',
      'title: "edit test/system/checkout_test.rb"',
      'title: "bash bin/rails test test/system/checkout_test.rb"',
      'title: "write docs/setup.md"',
      'title: "edit app/views/checkouts/show.html.erb"',
      'title: "read .github/workflows/test.yml"',
      'title: "edit .github/workflows/test.yml"'
    ].each do |tool_title|
      assert_includes javascript, tool_title
    end

    assert_includes javascript, "Files changed"
    refute_includes javascript.split('{ id: "release-notes"', 2).last.downcase, "subagent"
  end

  def test_guide_sessions_do_not_use_native_tool_transcripts
    javascript = File.read(JAVASCRIPT)
    guide_sample = javascript[/const initialSessions = \[.*?\n    \{ id: "release-notes"/m]

    refute_nil guide_sample
    refute_match(/title: "(?:bash|read|write|edit)\b/, guide_sample)
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

  def test_demo_has_an_accessible_first_visit_intro
    body = Nokogiri::HTML5(File.read(HTML)).at_css("body")
    modal = body.at_css('[data-modal="demo-intro-modal"]')
    dialog = modal&.at_css('[role="dialog"][aria-modal="true"][aria-labelledby="demo-intro-title"]')
    repository_link = modal&.at_css('a[href="https://github.com/melounvitek/gripi"]')

    refute_nil dialog
    assert modal.key?("hidden")
    assert_equal "Welcome to Gripi", body.at_css("#demo-intro-title").text
    assert_includes dialog.text, "web and desktop interface for Pi"
    assert_includes dialog.text, "Pi stays Pi"
    assert_includes dialog.text, "simulated"
    assert_includes dialog.text, "stay in this browser"
    refute_includes dialog.text, "Does this look 1:1 realistic as the real product?"
    refute_includes dialog.text, "not as polished here as they are in the real app"
    explore_action = dialog.at_css('button[data-modal-close][data-modal-default-focus]')
    assert explore_action, "Expected an Explore demo action"
    assert_equal "Explore demo", explore_action.text.strip
    refute_nil repository_link
    refute repository_link.attribute("target")
    assert body.at_css('[data-modal-open="demo-intro-modal"]'), "Expected an About this demo reopen control"
  end

  def test_demo_intro_is_remembered_independently_of_versioned_session_state
    javascript = File.read(JAVASCRIPT)

    assert_includes javascript, 'const introSeenKey = "gripi:static-demo:intro-seen";'
    assert_includes javascript, 'localStorage.getItem(introSeenKey) === "true"'
    assert_includes javascript, 'localStorage.setItem(introSeenKey, "true")'
    assert_includes javascript, 'if (!introSeen()) openModal("demo-intro-modal", null);'
    refute_includes javascript, 'openModal("demo-intro-modal", element.prompt)'
    assert_includes javascript, 'if (modal.dataset.modal === "demo-intro-modal") markIntroSeen();'
    assert_includes javascript, 'document.querySelector(".app-shell").inert = true'
    assert_includes javascript, 'document.querySelector(".app-shell").inert = false'
    assert_includes javascript, '&& !modalIsOpen()'
    refute_match(/gripi:static-demo:v\d+:intro-seen/, javascript)
  end

  def test_demo_has_no_global_notice_bar
    body = Nokogiri::HTML5(File.read(HTML)).at_css("body")
    javascript = File.read(JAVASCRIPT)

    refute body.at_css("#demo-notice")
    refute_includes javascript, "showDemoNotice"
    refute_includes javascript, "data-dismiss-notice"
    assert_includes javascript, 'if (event.target.closest("[data-demo-disabled]")) event.preventDefault();'
  end

  def test_demo_mobile_composer_starts_clean_and_compact
    html = File.read(HTML)
    body = Nokogiri::HTML5(html).at_css("body")

    attachment_tray = body.at_css(".attachment-tray")
    attach_input = body.at_css("#image-input")
    attach_button = body.at_css("button.attach-button")
    refute attachment_tray["class"].to_s.split.include?("has-attachments")
    assert_empty attachment_tray.css(".attachment")
    assert attach_input.key?("disabled")
    assert_includes attach_button["class"], "is-disabled"
    assert_equal "button", attach_button["type"]
    assert_equal "Learn why image uploads are disabled in this demo", attach_button["aria-label"]
    assert_equal "demo-images-modal", attach_button["data-modal-open"]
    assert_includes html, ".attach-button.is-disabled[data-modal-open] { pointer-events: auto; cursor: help; }"
    assert_equal "Ask Pi…", body.at_css('.prompt-form textarea')["placeholder"]
    assert_includes html, "@media (max-width: 760px)"
    assert_includes html, ".composer-controls { display: none; }"
  end

  def test_demo_uses_codex_model_label
    html = File.read(HTML)
    body = Nokogiri::HTML5(html).at_css("body")

    assert_equal "openai-codex/gpt-5.5 (medium)", body.at_css('[data-status-key="model"] .session-status-value').text
    assert body.at_css('input[name="model"][value="openai-codex/gpt-5.5"][checked]')
    assert body.at_css('input[name="thinking"][value="medium"][checked]')
    refute_includes html, "claude-sonnet"
  end

  def test_demo_image_attachment_note_explains_static_demo_limitation
    body = Nokogiri::HTML5(File.read(HTML)).at_css("body")
    modal = body.at_css('[data-modal="demo-images-modal"]')
    dialog = modal&.at_css('[role="dialog"][aria-modal="true"][aria-labelledby="demo-images-title"]')

    refute_nil dialog
    assert modal.key?("hidden")
    assert_equal "Images are supported in Gripi", body.at_css("#demo-images-title").text
    assert_includes dialog.text, "A connected Gripi gateway can send image attachments to Pi"
    assert_includes dialog.text, "when the selected model supports images"
    assert_includes dialog.text, "This static demo runs entirely in your browser"
    assert_equal "Got it", dialog.at_css('button[data-modal-close][data-modal-default-focus]').text.strip
  end

  def test_streams_are_bound_to_the_originating_session_and_blocked_while_switching
    javascript = File.read(JAVASCRIPT)

    assert_includes javascript, "if (!prompt || streamController || switching) return;"
    assert_includes javascript, "appendStreamEvent(event, streamSession)"
    assert_includes javascript, "if (generation !== switchGeneration) return;"
  end

  def test_jump_arrows_only_appear_for_intentional_scroll_direction
    result = run_javascript(<<~JS)
      console.log(JSON.stringify({
        up: GripiDemo.jumpControlVisibility(500, 300, 900),
        down: GripiDemo.jumpControlVisibility(300, 500, 900),
        downLatestVisible: GripiDemo.jumpControlVisibility(300, 500, 900, true),
        unchanged: GripiDemo.jumpControlVisibility(500, 500, 900),
        top: GripiDemo.jumpControlVisibility(200, 50, 900),
        bottom: GripiDemo.jumpControlVisibility(700, 850, 900)
      }));
    JS

    assert_equal({ "top" => true, "bottom" => false }, result.fetch("up"))
    assert_equal({ "top" => false, "bottom" => true }, result.fetch("down"))
    assert_equal({ "top" => false, "bottom" => false }, result.fetch("downLatestVisible"))
    assert_nil result.fetch("unchanged")
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

  def test_demo_assistant_text_supports_inline_code_markup
    result = run_javascript(<<~JS)
      console.log(JSON.stringify(GripiDemo.inlineCodeParts("Use `Gemfile.lock`, not `github.sha`.")));
    JS

    assert_equal [
      { "type" => "text", "text" => "Use " },
      { "type" => "code", "text" => "Gemfile.lock" },
      { "type" => "text", "text" => ", not " },
      { "type" => "code", "text" => "github.sha" },
      { "type" => "text", "text" => "." }
    ], result
  end

  def test_demo_tool_activity_uses_production_compact_structure_without_fake_output
    javascript = File.read(JAVASCRIPT)

    assert_includes javascript, 'role === "tool" ? " message--compact message--tool-call"'
    assert_includes javascript, 'details.className = "message-details message-details--always-open"'
    assert_includes javascript, 'summary.className = "message-details-summary"'
    assert_includes javascript, 'compact.className = "compact-summary"'
    refute_includes javascript, "dataToolOutputBody"
    refute_includes javascript, "dataToolOutputToggle"
    refute_includes javascript, "tool-output-content--diff"
    refute_includes javascript, "toolOutputModel"
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
