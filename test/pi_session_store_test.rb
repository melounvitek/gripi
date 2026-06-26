require "minitest/autorun"
require "base64"
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

  def test_preserves_image_blocks_and_image_only_user_messages
    Dir.mktmpdir do |dir|
      session_dir = File.join(dir, "--project--")
      FileUtils.mkdir_p(session_dir)
      path = File.join(session_dir, "session.jsonl")
      image_data = Base64.strict_encode64("fake image data")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "message", message: { role: "user", content: [{ type: "image", data: image_data, mimeType: "image/png" }] } }
      ])

      messages = PiSessionStore.new(root: dir).messages(path)

      assert_equal 1, messages.length
      assert_equal "user", messages.first.role
      assert_equal "", messages.first.text
      assert_equal [{ data: image_data, mime_type: "image/png" }], messages.first.images
    end
  end

  def test_exposes_parent_session_path
    Dir.mktmpdir do |dir|
      session_dir = File.join(dir, "--project--")
      FileUtils.mkdir_p(session_dir)
      parent_path = File.join(session_dir, "parent.jsonl")
      child_path = File.join(session_dir, "child.jsonl")
      write_jsonl(parent_path, [
        { type: "session", id: "parent", cwd: "/tmp/project" }
      ])
      write_jsonl(child_path, [
        { type: "session", id: "child", cwd: "/tmp/project", parentSession: parent_path }
      ])

      child = PiSessionStore.new(root: dir).sessions.find { |session| session.path == child_path }

      assert_equal parent_path, child.parent_session_path
    end
  end

  def test_exposes_latest_assistant_response_preview
    Dir.mktmpdir do |dir|
      session_dir = File.join(dir, "--project--")
      FileUtils.mkdir_p(session_dir)
      path = File.join(session_dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "message", message: { role: "assistant", content: [{ type: "thinking", thinking: "private" }] } },
        { type: "message", message: { role: "assistant", content: [{ type: "toolCall", name: "bash", arguments: { command: "echo hi" } }] } },
        { type: "message", message: { role: "assistant", content: [{ type: "text", text: "Older answer" }] } },
        { type: "message", message: { role: "assistant", content: [{ type: "text", text: "Latest\nanswer" }] } }
      ])

      session = PiSessionStore.new(root: dir).sessions.first

      assert_equal "Latest answer", session.latest_assistant_response_preview
    end
  end

  def test_exposes_latest_compaction_activity_preview
    Dir.mktmpdir do |dir|
      session_dir = File.join(dir, "--project--")
      FileUtils.mkdir_p(session_dir)
      path = File.join(session_dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "message", message: { role: "assistant", content: [{ type: "text", text: "Older answer" }] } },
        { type: "compaction", timestamp: "2026-06-13T10:01:00Z", summary: "Important summary\nwith details" }
      ])

      session = PiSessionStore.new(root: dir).sessions.first

      assert_equal "compaction", session.latest_activity_kind
      assert_equal "Conversation compacted", session.latest_activity_title
      assert_equal "Important summary with details", session.latest_activity_preview
    end
  end

  def test_later_assistant_response_replaces_compaction_activity_preview
    Dir.mktmpdir do |dir|
      session_dir = File.join(dir, "--project--")
      FileUtils.mkdir_p(session_dir)
      path = File.join(session_dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "compaction", timestamp: "2026-06-13T10:01:00Z", summary: "Important summary" },
        { type: "message", timestamp: "2026-06-13T10:02:00Z", message: { role: "assistant", content: [{ type: "text", text: "Latest answer" }] } }
      ])

      session = PiSessionStore.new(root: dir).sessions.first

      assert_equal "assistant", session.latest_activity_kind
      assert_equal "Latest answer", session.latest_activity_preview
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

  def test_reads_compaction_entries_as_compact_status_messages
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        {
          type: "compaction",
          timestamp: "2026-06-13T10:00:00Z",
          summary: "Important summary",
          tokensBefore: 1234
        }
      ])

      messages = PiSessionStore.new(root: dir).messages(path)

      assert_equal 1, messages.length
      assert_equal "status", messages.first.role
      assert_equal "Important summary", messages.first.text
      assert_equal "Conversation compacted", messages.first.summary
      assert messages.first.compact
      assert_equal Time.parse("2026-06-13T10:00:00Z"), messages.first.timestamp
    end
  end

  def test_reads_messages_for_a_selected_session
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "message", id: "user-entry-1", message: { role: "user", content: [{ type: "text", text: "Hello" }] } },
        { type: "message", message: { role: "assistant", content: [{ type: "text", text: "Hi" }] } }
      ])

      messages = PiSessionStore.new(root: dir).messages(path)

      assert_equal ["user", "assistant"], messages.map(&:role)
      assert_equal ["Hello", "Hi"], messages.map(&:text)
      assert_equal "user-entry-1", messages.first.entry_id
    end
  end

  def test_estimates_latest_status_from_newer_compaction_entry
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      summary = "Compacted summary text"
      kept_text = "Retained conversation text"
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "message", id: "old-entry", timestamp: "2026-06-13T10:00:00Z", message: { role: "assistant", content: [{ type: "text", text: "Old answer" }], usage: { totalTokens: 12_345 } } },
        { type: "message", id: "kept-entry", timestamp: "2026-06-13T10:01:00Z", message: { role: "user", content: [{ type: "text", text: kept_text }] } },
        { type: "compaction", timestamp: "2026-06-13T10:02:00Z", summary: summary, firstKeptEntryId: "kept-entry", tokensBefore: 12_345 }
      ])

      status = PiSessionStore.new(root: dir).status(path)

      assert status.context_estimated
      assert_operator status.context_tokens, :<, 12_345
      assert_equal ([summary, kept_text].join("\n").length / 4.0).ceil, status.context_tokens
    end
  end

  def test_estimates_compaction_status_from_summary_when_first_kept_entry_is_missing
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      summary = "Summary only"
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "model_change", provider: "openai-codex", modelId: "gpt-5.5" },
        { type: "message", id: "old-entry", timestamp: "2026-06-13T10:00:00Z", message: { role: "assistant", content: [{ type: "text", text: "Old answer" * 200 }], usage: { totalTokens: 12_345 } } },
        { type: "compaction", timestamp: "2026-06-13T10:02:00Z", summary: summary, tokensBefore: 12_345 }
      ])

      status = PiSessionStore.new(root: dir).status(path)

      assert status.context_estimated
      assert_equal (summary.length / 4.0).ceil, status.context_tokens
    end
  end

  def test_keeps_later_usage_when_compaction_has_same_timestamp
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "compaction", timestamp: "2026-06-13T10:02:00Z", summary: "Summary", tokensBefore: 12_345 },
        { type: "message", timestamp: "2026-06-13T10:02:00Z", message: { role: "assistant", content: [{ type: "text", text: "New answer" }], usage: { totalTokens: 999, contextWindow: 1000 } } }
      ])

      status = PiSessionStore.new(root: dir).status(path)

      refute status.context_estimated
      assert_equal 999, status.context_tokens
      assert_equal 1000, status.context_limit
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
