require "minitest/autorun"
require "json"
require "open3"

class LiveStreamingJsTest < Minitest::Test
  ASSETS = File.expand_path("../public/assets", __dir__)

  def test_parser_preserves_supported_images_and_interprets_thinking_and_tools
    result = run_javascript(<<~JS)
      const { LiveMessageParser } = await import(#{module_url("live_message_parser.js").to_json});
      const parser = new LiveMessageParser("/home/tester");
      const segments = parser.contentSegments([
        { type: "thinking", thinking: "**Reasoning**\\n\\nInspect this" },
        { type: "toolCall", name: "read", id: "read-1", arguments: { path: "/home/tester/file", offset: 2, limit: 3 } },
        { type: "image", data: "cG5n", mimeType: "image/png" },
        { type: "image", data: "c3Zn", mimeType: "image/svg+xml" }
      ], { role: "assistant" });
      console.log(JSON.stringify(segments));
    JS

    assert_equal 2, result.length
    assert_equal true, result[0]["thinking"]
    assert_equal "Inspect this", result[0]["text"]
    assert_equal({ "name" => "read", "path" => "~/file", "range" => "2-4" }, result[1]["summaryParts"])
    assert_equal ["data:image/png;base64,cG5n"], result[1]["images"].map { |image| image["src"] }
  end

  def test_renderer_streaming_state_is_cleared_before_a_new_assistant_message
    result = run_javascript(<<~JS)
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      const removed = [];
      const stale = article("stale");
      const tracked = article("tracked");
      const renderer = Object.create(LiveMessageRenderer.prototype);
      renderer.conversationScroll = { querySelectorAll: () => [stale] };
      renderer.liveAssistantSegments = new Map([["one", { article: tracked }]]);
      renderer.livePairedToolCalls = new Map();
      renderer.liveToolExecutions = new Map();
      renderer.liveUserMessages = new Map();
      renderer.clearLiveAssistantStreaming();
      renderer.resetLiveAssistantTracking();
      console.log(JSON.stringify({ removed, segments: renderer.liveAssistantSegments.size, seen: renderer.liveAssistantSeen }));
      function article(name) { return { classList: { remove(value) { removed.push([name, value]); } } }; }
    JS

    assert_equal [["stale", "message--streaming"], ["tracked", "message--streaming"]], result["removed"]
    assert_equal 0, result["segments"]
    assert_equal false, result["seen"]
  end

  def test_renderer_replaces_live_message_images
    result = run_javascript(<<~JS)
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      const renderer = Object.create(LiveMessageRenderer.prototype);
      renderer.document = { createElement: (tagName) => ({ tagName, children: [], append(...children) { this.children.push(...children); }, addEventListener() {} }) };
      let container = null;
      const article = { querySelector: () => container, append(value) { container = value; } };
      renderer.replaceMessageImages(article, [{ src: "data:image/png;base64,cG5n", alt: "Attached image" }]);
      const firstCount = container.children.length;
      container.remove = () => { container = null; };
      renderer.replaceMessageImages(article, []);
      console.log(JSON.stringify({ firstCount, cleared: container === null }));
    JS

    assert_equal 1, result["firstCount"]
    assert_equal true, result["cleared"]
  end

  def test_renderer_builds_compact_tool_output_lines_through_its_bound_document
    result = run_javascript(<<~JS)
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      const renderer = Object.create(LiveMessageRenderer.prototype);
      renderer.parser = { displayHomePath: (text) => text.replace("/home/tester", "~") };
      renderer.document = { createElement: () => ({ className: "", children: [], classList: { add() {} }, textContent: "", append(...children) { this.children.push(...children); } }) };
      const body = {
        dataset: {},
        classList: { toggle() {} },
        closest: () => null,
        replaceChildren(...children) { this.children = children; }
      };
      renderer.renderToolTranscriptBody(body, "/home/tester/one\\n/home/tester/two", "read");
      console.log(JSON.stringify({ wrapperClass: body.children[0].className, lines: body.children[0].children.map((child) => child.textContent) }));
    JS

    assert_equal "tool-output-content", result["wrapperClass"]
    assert_equal ["~/one", "~/two"], result["lines"]
  end

  def test_optimistic_uploaded_images_remain_owned_by_renderer
    script = File.read(File.join(ASSETS, "live_message_renderer.js"))
    app = File.read(File.join(ASSETS, "app.js"))

    assert_includes script, "article.dataset.optimisticImageCount = String(options.images?.length || 0);"
    assert_includes script, 'return targetText.startsWith(`${optimisticText}\\n`);'
    assert_includes app, "const optimisticImages = pendingImages.map"
    assert_includes app, "liveMessageRenderer.appendMessage(\"user\""
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
