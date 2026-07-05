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
