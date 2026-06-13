require "minitest/autorun"
require "tmpdir"
require "json"
require "fileutils"
require_relative "../lib/pi_session_store"

class PiSessionStoreTest < Minitest::Test
  def test_lists_sessions_with_metadata_grouped_by_cwd
    Dir.mktmpdir do |dir|
      session_dir = File.join(dir, "--project--")
      FileUtils.mkdir_p(session_dir)
      path = File.join(session_dir, "2026-06-13T10-00-00-000Z_abc.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", timestamp: "2026-06-13T10:00:00Z", cwd: "/tmp/project" },
        { type: "message", message: { role: "user", content: [{ type: "text", text: "First prompt" }] } },
        { type: "session_info", name: "Named session" },
        { type: "message", message: { role: "assistant", content: [{ type: "text", text: "Answer" }] } }
      ])

      store = PiSessionStore.new(root: dir)

      groups = store.grouped_sessions
      assert_equal ["/tmp/project"], groups.keys
      session = groups["/tmp/project"].first
      assert_equal path, session.path
      assert_equal "session-1", session.id
      assert_equal "Named session", session.display_name
      assert_equal "First prompt", session.first_user_message
      assert_equal 2, session.message_count
      assert session.created_at
      assert session.modified_at
    end
  end

  def test_uses_first_user_message_when_session_has_no_name
    Dir.mktmpdir do |dir|
      session_dir = File.join(dir, "--project--")
      FileUtils.mkdir_p(session_dir)
      path = File.join(session_dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "message", message: { role: "user", content: [{ type: "text", text: "Fallback title" }] } }
      ])

      session = PiSessionStore.new(root: dir).sessions.first

      assert_equal "Fallback title", session.display_name
    end
  end

  def test_splits_mixed_assistant_thinking_from_visible_text
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        {
          type: "message",
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "assistant",
            content: [
              { type: "thinking", thinking: "**Heading**\n\nPrivate reasoning" },
              { type: "text", text: "## Visible answer" }
            ]
          }
        }
      ])

      messages = PiSessionStore.new(root: dir).messages(path)

      assert_equal ["assistant", "assistant"], messages.map(&:role)
      assert_equal ["Private reasoning", "## Visible answer"], messages.map(&:text)
      assert_equal [false, false], messages.map(&:compact)
      assert_equal [true, false], messages.map(&:thinking)
      assert_nil messages.first.summary
    end
  end

  def test_reads_messages_for_a_selected_session
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "message", message: { role: "user", content: [{ type: "text", text: "Hello" }] } },
        { type: "message", message: { role: "assistant", content: [{ type: "text", text: "Hi" }] } }
      ])

      messages = PiSessionStore.new(root: dir).messages(path)

      assert_equal ["user", "assistant"], messages.map(&:role)
      assert_equal ["Hello", "Hi"], messages.map(&:text)
    end
  end

  def test_reads_latest_status_from_session_entries
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "model_change", provider: "openai-codex", modelId: "gpt-5.5" },
        { type: "thinking_level_change", thinkingLevel: "medium" },
        { type: "message", message: { role: "assistant", content: [{ type: "text", text: "Hi" }], usage: { totalTokens: 12_345, cost: { total: 0.123 } } } }
      ])

      status = PiSessionStore.new(root: dir).status(path)

      assert_equal "openai-codex", status.provider
      assert_equal "gpt-5.5", status.model_id
      assert_equal "medium", status.thinking_level
      assert_equal 12_345, status.context_tokens
      assert_equal 0.123, status.cost_total
    end
  end

  private

  def write_jsonl(path, entries)
    File.write(path, entries.map { |entry| JSON.generate(entry) }.join("\n"))
  end
end
