require "minitest/autorun"
require "open3"

class LiveStreamingJsTest < Minitest::Test
  VIEW_PATH = File.expand_path("../views/index.erb", __dir__)

  def test_streaming_cleanup_removes_stale_dom_cursors
    script = File.read(VIEW_PATH)

    assert_includes script, 'querySelectorAll(".message--assistant.message--streaming")'
  end

  def test_new_assistant_message_clears_stale_streaming_before_tracking_reset
    assert_cleanup_before_reset_in('if (roleName === "assistant" && event.type === "message_start")')
  end

  def test_terminal_events_clear_stale_streaming_before_tracking_reset
    assert_cleanup_before_reset_in('if (event.type === "turn_end")')
    assert_cleanup_before_reset_in('if (renderErrorEvent(event))')
    assert_cleanup_before_reset_in('if (!liveErrorSeen) {', after: 'if (event.type === "agent_end")')
  end

  def test_thinking_segments_are_rendered_as_markdown
    script = File.read(VIEW_PATH)

    assert_includes script, 'options.thinking ? "message-body message-body--thinking message-body--markdown"'
    assert_includes script, "if (roleName === \"assistant\") {\n        renderAssistantMarkdown(body, text);"
    assert_includes script, "if (roleName === \"assistant\") {\n          renderAssistantMarkdown(entry.body, segment.text);"
  end

  def test_live_read_results_preserve_supported_images
    script = File.read(VIEW_PATH)
    formatter_start = script.index("    function compactContentPart")
    formatter_end = script.index("    function eventMessage", formatter_start)
    formatter = script[formatter_start...formatter_end]
    assertion = <<~JS
      const segments = contentSegments([
        { type: "text", text: "Read image file [image/png]" },
        { type: "image", data: "cG5n", mimeType: "image/png" },
        { type: "image", data: "c3Zn", mimeType: "image/svg+xml" }
      ], { role: "toolResult", toolName: "read", toolCallId: "read-1" });
      if (segments.length !== 1) process.exit(1);
      if (segments[0].images.length !== 1) process.exit(2);
      if (segments[0].images[0].src !== "data:image/png;base64,cG5n") process.exit(3);
    JS

    refute_nil formatter_start
    refute_nil formatter_end
    _stdout, stderr, status = Open3.capture3("node", stdin_data: "const HOME_DIR = '';\n" + formatter + assertion)

    assert status.success?, stderr
  end

  def test_live_read_results_are_rendered_on_the_existing_tool_card
    script = File.read(VIEW_PATH)
    paired_result = script.index("else if (pairedToolCallEntry && segment.isToolResult)")
    next_branch = script.index("else if (roleName === \"user\"", paired_result)

    assert_includes script[paired_result...next_branch], "replaceMessageImages(pairedToolCallEntry.article, segment.images);"
  end

  def test_live_message_images_are_replaced_and_cleared
    script = File.read(VIEW_PATH)
    renderer_start = script.index("    function replaceMessageImages")
    renderer_end = script.index("    function appendMessage", renderer_start)
    renderer = script[renderer_start...renderer_end]
    assertion = <<~JS
      let currentContainer = null;
      global.document = {
        createElement(tagName) {
          return {
            tagName,
            children: [],
            append(...children) { this.children.push(...children); },
            addEventListener() {},
            remove() { if (currentContainer === this) currentContainer = null; }
          };
        }
      };
      const article = {
        querySelector() { return currentContainer; },
        append(container) { currentContainer = container; }
      };
      const image = { src: "data:image/png;base64,cG5n", alt: "Attached image" };
      replaceMessageImages(article, [image]);
      replaceMessageImages(article, [image]);
      if (!currentContainer || currentContainer.children.length !== 1) process.exit(1);
      replaceMessageImages(article, []);
      if (currentContainer) process.exit(2);
    JS

    refute_nil renderer_start
    refute_nil renderer_end
    _stdout, stderr, status = Open3.capture3("node", stdin_data: renderer + assertion)

    assert status.success?, stderr
  end

  def test_optimistic_user_message_can_render_uploaded_image_previews
    script = File.read(VIEW_PATH)

    assert_includes script, "function renderMessageImages(article, images = [])"
    assert_includes script, "article.dataset.optimisticImageCount = String(options.images?.length || 0);"
    assert_includes script, 'return targetText.startsWith(`${optimisticText}\\n`);'
    assert_includes script, "renderMessageImages(article, options.images);"
    assert_includes script, "const optimisticImages = pendingImages.map((entry) => ({ src: URL.createObjectURL(entry.file), alt: entry.file.name || \"Attached image\" }));"
    assert_includes script, "images: optimisticImages"
  end

  private

  def assert_cleanup_before_reset_in(block_start, after: nil)
    script = File.read(VIEW_PATH)
    search_start = after ? script.index(after) : 0
    block_index = script.index(block_start, search_start)
    cleanup = script.index("clearLiveAssistantStreaming();", block_index)
    reset = script.index("resetLiveAssistantTracking();", block_index)

    refute_nil cleanup
    refute_nil reset
    assert_operator cleanup, :<, reset
  end
end
