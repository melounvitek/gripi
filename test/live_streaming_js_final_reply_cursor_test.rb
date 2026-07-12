require "minitest/autorun"

class LiveStreamingJsFinalReplyCursorTest < Minitest::Test
  RENDERER_PATH = File.expand_path("../public/assets/live_message_renderer.js", __dir__)

  def test_thinking_segments_do_not_get_streaming_cursor
    script = File.read(RENDERER_PATH)

    assert_includes script, 'const streamingAssistantResponse = event.type !== "message_end" && !segment.compact && !segment.thinking;'
    refute_includes File.read(File.expand_path("../public/assets/app.css", __dir__)), ".message-body--thinking::after"
  end
end
