require "minitest/autorun"

class LiveStreamingJsFinalReplyCursorTest < Minitest::Test
  VIEW_PATH = File.expand_path("../views/index.erb", __dir__)

  def test_thinking_segments_do_not_get_streaming_cursor
    script = File.read(VIEW_PATH)

    assert_includes script, 'const streamingAssistantResponse = event.type !== "message_end" && !segment.compact && !segment.thinking;'
    refute_includes script, ".message-body--thinking::after"
  end
end
