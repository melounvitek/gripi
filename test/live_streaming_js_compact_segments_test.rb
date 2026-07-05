require "minitest/autorun"

class LiveStreamingJsCompactSegmentsTest < Minitest::Test
  VIEW_PATH = File.expand_path("../views/index.erb", __dir__)

  def test_assistant_tool_call_segments_clear_final_reply_cursor
    script = File.read(VIEW_PATH)
    assistant_segments = script.index("liveAssistantSeen = true;")
    compact_cleanup = script.index("if (segment.compact) clearLiveAssistantStreaming();", assistant_segments)
    upsert = script.index("const entry = upsertLiveAssistantSegment(event, roleName, segment, index, shouldScroll, timestamp);", assistant_segments)

    refute_nil compact_cleanup
    refute_nil upsert
    assert_operator compact_cleanup, :<, upsert
  end
end
