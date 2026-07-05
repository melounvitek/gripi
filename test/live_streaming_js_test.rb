require "minitest/autorun"

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
