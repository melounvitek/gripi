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
      assert_equal Time.iso8601("2026-06-13T10:00:00Z"), session.conversation_activity_at
    end
  end

  def test_session_metadata_skips_canonical_tool_results_and_falls_back_for_other_layouts
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      canonical_tool_result_text = "x" * 100_000
      later_user_text = "y" * 300_000
      canonical_tool_result = JSON.generate({
        type: "message",
        id: "result-1",
        parentId: "user-1",
        timestamp: "2026-06-13T10:01:00Z",
        message: { role: "toolResult", content: [{ type: "text", text: canonical_tool_result_text }] }
      })
      reordered_tool_result = JSON.generate({
        type: "message",
        id: "result-2",
        message: { content: [{ type: "text", text: "fallback" }], role: "toolResult" }
      })
      File.write(path, [
        JSON.generate({ type: "session", id: "session-1", timestamp: "2026-06-13T10:00:00Z", cwd: "/tmp/project" }),
        JSON.generate({ type: "message", id: "user-1", message: { role: "user", content: [{ type: "text", text: "First prompt" }] } }),
        canonical_tool_result,
        JSON.generate({ type: "message", id: "user-2", parentId: "result-1", timestamp: "2026-06-13T10:02:00Z", message: { role: "user", content: [{ type: "text", text: later_user_text }] } }),
        reordered_tool_result
      ].join("\n"))
      parsed_lines = []
      parse = JSON.method(:parse)
      store = PiSessionStore.new(root: dir)

      replace_singleton_method(JSON, :parse, ->(line, *args) { parsed_lines << line; parse.call(line, *args) }) do
        session = store.sessions.first

        assert_equal "First prompt", session.display_name
        assert_equal 2, session.message_count
      end

      refute parsed_lines.include?(canonical_tool_result)
      refute parsed_lines.any? { |line| line.include?(later_user_text) }
      assert parsed_lines.include?(reordered_tool_result)
      assert store.messages(path).any? { |message| message.text == canonical_tool_result_text }
    end
  end

  def test_concurrent_metadata_cache_misses_read_a_session_once
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", timestamp: "2026-06-13T10:00:00Z", cwd: "/tmp/project" },
        { type: "message", id: "user-1", message: { role: "user", content: [{ type: "text", text: "First prompt" }] } }
      ])
      read_count, results = concurrent_session_reads(PiSessionStore.new(root: dir))
      timestamp = Time.iso8601("2026-06-13T10:00:00Z")
      expected = {
        path: path,
        cwd: "/tmp/project",
        id: "session-1",
        display_name: "First prompt",
        first_user_message: "First prompt",
        message_count: 1,
        assistant_response_count: 0,
        latest_assistant_response_preview: nil,
        latest_activity_kind: nil,
        latest_activity_title: nil,
        latest_activity_preview: nil,
        parent_session_path: nil,
        created_at: timestamp,
        modified_at: File.mtime(path),
        conversation_activity_at: timestamp
      }

      assert_equal 1, read_count
      assert_equal [expected, expected], results.map { |sessions| sessions.fetch(0).to_h }
    end
  end

  def test_concurrent_empty_metadata_cache_misses_are_shared_and_invalidated_after_append
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      File.write(path, JSON.generate({ type: "message", id: "user-1", message: { role: "user", content: [] } }) + "\n")
      store = PiSessionStore.new(root: dir)

      read_count, results = concurrent_session_reads(store)

      assert_equal 1, read_count
      assert_equal [[], []], results

      File.open(path, "a") do |file|
        file.puts JSON.generate({ type: "session", id: "session-1", timestamp: "2026-06-13T10:00:00Z", cwd: "/tmp/project" })
      end
      assert_equal ["session-1"], store.sessions.map(&:id)
    end
  end

  def test_empty_latest_session_name_restores_first_message_fallback
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", timestamp: "2026-06-13T10:00:00Z", cwd: "/tmp/project" },
        { type: "message", message: { role: "user", content: [{ type: "text", text: "First prompt" }] } },
        { type: "session_info", name: "Named session" },
        { type: "session_info", name: "" }
      ])

      session = PiSessionStore.new(root: dir).sessions.first

      assert_equal "First prompt", session.display_name
    end
  end

  def test_conversation_activity_ignores_non_conversation_session_writes
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", timestamp: "2026-06-13T10:00:00Z", cwd: "/tmp/project" },
        { type: "message", timestamp: "2026-06-13T10:01:00Z", message: { role: "user", content: [{ type: "text", text: "Please investigate" }] } },
        { type: "message", timestamp: "2026-06-13T10:02:00Z", message: { role: "assistant", stopReason: "stop", content: [{ type: "text", text: "Final reply" }] } },
        { type: "message", timestamp: "2026-06-13T10:03:00Z", message: { role: "assistant", stopReason: "toolUse", content: [{ type: "text", text: "I will inspect it" }, { type: "toolCall", name: "read" }] } },
        { type: "message", timestamp: "2026-06-13T10:04:00Z", message: { role: "toolResult", content: [{ type: "text", text: "Result" }] } },
        { type: "compaction", timestamp: "2026-06-13T10:05:00Z", summary: "Summary" },
        { type: "session_info", timestamp: "2026-06-13T10:06:00Z", name: "Renamed" },
        { type: "custom", timestamp: "2026-06-13T10:07:00Z", customType: "extension", data: {} }
      ])
      FileUtils.touch(path, mtime: Time.local(2026, 6, 13, 12, 0, 0))

      session = PiSessionStore.new(root: dir).sessions.first

      assert_equal Time.iso8601("2026-06-13T10:02:00Z"), session.conversation_activity_at
      assert_equal Time.local(2026, 6, 13, 12, 0, 0), session.modified_at
    end
  end

  def test_conversation_activity_accepts_completed_length_limited_and_legacy_assistant_replies
    Dir.mktmpdir do |dir|
      expected_time = Time.iso8601("2026-06-13T10:01:00Z")

      ["stop", "length", nil].each_with_index do |stop_reason, index|
        path = File.join(dir, "accepted-#{index}.jsonl")
        message = { role: "assistant", content: [{ type: "text", text: "Visible reply" }] }
        message[:stopReason] = stop_reason if stop_reason
        write_jsonl(path, [
          { type: "session", id: "accepted-#{index}", timestamp: "2026-06-13T10:00:00Z", cwd: "/tmp/project" },
          { type: "message", timestamp: "2026-06-13T10:01:00Z", message: message }
        ])
      end

      assert_equal [expected_time], PiSessionStore.new(root: dir).sessions.map(&:conversation_activity_at).uniq
    end
  end

  def test_conversation_activity_excludes_unfinished_or_non_text_assistant_messages
    Dir.mktmpdir do |dir|
      header_time = Time.iso8601("2026-06-13T10:00:00Z")
      messages = [
        { role: "assistant", stopReason: "toolUse", content: [{ type: "text", text: "Progress" }] },
        { role: "assistant", stopReason: "aborted", content: [{ type: "text", text: "Partial reply" }] },
        { role: "assistant", stopReason: "error", content: [{ type: "text", text: "Failed reply" }] },
        { role: "assistant", stopReason: "stop", content: [{ type: "thinking", thinking: "Private" }] }
      ]

      messages.each_with_index do |message, index|
        write_jsonl(File.join(dir, "excluded-#{index}.jsonl"), [
          { type: "session", id: "excluded-#{index}", timestamp: "2026-06-13T10:00:00Z", cwd: "/tmp/project" },
          { type: "message", timestamp: "2026-06-13T10:01:00Z", message: message }
        ])
      end

      assert_equal [header_time], PiSessionStore.new(root: dir).sessions.map(&:conversation_activity_at).uniq
    end
  end

  def test_image_only_user_message_counts_as_conversation_activity
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", timestamp: "2026-06-13T10:00:00Z", cwd: "/tmp/project" },
        { type: "message", timestamp: "2026-06-13T10:01:00Z", message: { role: "user", content: [{ type: "image", data: "abc", mimeType: "image/png" }] } }
      ])

      session = PiSessionStore.new(root: dir).sessions.first

      assert_equal Time.iso8601("2026-06-13T10:01:00Z"), session.conversation_activity_at
    end
  end

  def test_lists_sessions_by_conversation_activity_instead_of_file_modification
    Dir.mktmpdir do |dir|
      older_conversation = File.join(dir, "older-conversation.jsonl")
      newer_conversation = File.join(dir, "newer-conversation.jsonl")
      write_jsonl(older_conversation, [
        { type: "session", id: "older", timestamp: "2026-06-13T10:00:00Z", cwd: "/tmp/project" },
        { type: "message", timestamp: "2026-06-13T10:01:00Z", message: { role: "user", content: [{ type: "text", text: "Older" }] } }
      ])
      write_jsonl(newer_conversation, [
        { type: "session", id: "newer", timestamp: "2026-06-13T10:00:00Z", cwd: "/tmp/project" },
        { type: "message", timestamp: "2026-06-13T10:02:00Z", message: { role: "user", content: [{ type: "text", text: "Newer" }] } }
      ])
      FileUtils.touch(older_conversation, mtime: Time.local(2026, 6, 13, 12, 0, 0))
      FileUtils.touch(newer_conversation, mtime: Time.local(2026, 6, 13, 11, 0, 0))

      sessions = PiSessionStore.new(root: dir).sessions

      assert_equal [newer_conversation, older_conversation], sessions.map(&:path)
    end
  end

  def test_hides_sessions_with_missing_cwds_without_deleting_them
    Dir.mktmpdir do |dir|
      missing_cwd = File.join(dir, "missing-project")
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: missing_cwd }
      ])
      store = PiSessionStore.new(root: dir, hide_missing_cwds: true)

      assert_empty store.sessions
      assert File.exist?(path)

      FileUtils.mkdir_p(missing_cwd)

      assert_equal [path], store.sessions.map(&:path)
    end
  end

  def test_reads_only_displayed_custom_messages
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      image_data = Base64.strict_encode64("fake image data")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        {
          type: "custom_message",
          id: "custom-1",
          timestamp: "2026-06-13T10:01:00Z",
          customType: "session-title-update",
          content: [
            { type: "text", text: "Session renamed" },
            { type: "image", data: image_data, mimeType: "image/png" }
          ],
          display: true
        },
        {
          type: "custom_message",
          id: "custom-2",
          timestamp: "2026-06-13T10:02:00Z",
          customType: "hidden-context",
          content: "Do not display",
          display: false
        }
      ])

      messages = PiSessionStore.new(root: dir).messages(path)

      assert_equal 1, messages.length
      message = messages.first
      assert_equal "custom", message.role
      assert_equal "Session renamed", message.text
      assert_equal "custom-1", message.entry_id
      assert_equal "session-title-update", message.custom_type
      assert_equal Time.iso8601("2026-06-13T10:01:00Z"), message.timestamp
      assert_equal [{ data: image_data, mime_type: "image/png" }], message.images
    end
  end

  def test_maps_trailing_editable_entries_to_their_stable_tree_position
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "message", id: "assistant-1", parentId: nil, message: { role: "assistant", content: [{ type: "text", text: "Answer" }] } },
        { type: "message", id: "user-1", parentId: "assistant-1", message: { role: "user", content: [{ type: "text", text: "Follow-up" }] } },
        { type: "custom_message", id: "custom-1", parentId: "user-1", customType: "session-title-update", content: "Session renamed", display: true }
      ])

      conversation = PiSessionStore.new(root: dir).conversation(path, current_leaf_id: "custom-1")

      assert_equal "assistant-1", conversation.latest_stable_tree_position_id
      assert_equal "assistant-1", conversation.current_stable_tree_position_id
      assert_equal ["Answer", "Follow-up", "Session renamed"], conversation.messages.map(&:text)
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

  def test_preserves_images_from_paired_read_results
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      image_data = Base64.strict_encode64("fake image data")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        {
          type: "message",
          message: {
            role: "assistant",
            content: [{ type: "toolCall", id: "read-1", name: "read", arguments: { path: "/tmp/screenshot.png" } }]
          }
        },
        {
          type: "message",
          message: {
            role: "toolResult",
            toolCallId: "read-1",
            toolName: "read",
            content: [
              { type: "text", text: "Read image file [image/png]" },
              { type: "image", data: image_data, mimeType: "image/png" }
            ]
          }
        }
      ])

      message = PiSessionStore.new(root: dir).messages(path).first

      assert_equal "read", message.tool_name
      assert_equal [{ data: image_data, mime_type: "image/png" }], message.images
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

  def test_only_valid_v1_commentary_is_excluded_from_assistant_responses
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      signature = ->(id, phase = nil) { JSON.generate({ v: 1, id: id, phase: phase }.compact) }
      texts = [
        ["Working on it", signature.call("progress", "commentary")],
        ["Signed answer", signature.call("answer", "final_answer")],
        ["Unsigned answer", nil],
        ["Opaque legacy answer", "opaque-signature"],
        ["Unphased answer", signature.call("unphased")],
        ["Malformed signature answer", "{broken"],
        ["Missing ID answer", JSON.generate(v: 1, phase: "commentary")],
        ["Future signature answer", JSON.generate(v: 2, id: "future", phase: "commentary")]
      ]
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        *texts.map { |text, text_signature| { type: "message", message: { role: "assistant", content: [{ type: "text", text: text, textSignature: text_signature }.compact] } } }
      ])

      store = PiSessionStore.new(root: dir)
      session = store.sessions.first
      messages = store.messages(path)

      assert_equal 7, session.assistant_response_count
      assert_equal "Future signature answer", session.latest_assistant_response_preview
      assert_equal [false, true, true, true, true, true, true, true], messages.map(&:final_assistant_response)
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

  def test_uses_the_subagent_tool_call_entry_timestamp_for_its_result
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        {
          type: "message",
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "assistant",
            content: [{ type: "toolCall", id: "call-1", name: "subagent", arguments: { agent: "reviewer", task: "Review the diff" } }]
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:05:00Z",
          message: {
            role: "toolResult",
            toolCallId: "call-1",
            toolName: "subagent",
            content: [{ type: "text", text: "No findings." }]
          }
        }
      ])

      store = PiSessionStore.new(root: dir)
      message = store.messages(path).first

      assert_equal "subagent", message.tool_name
      assert_equal "Review the diff", message.tool_prompt
      assert_equal Time.parse("2026-06-13T10:00:00Z"), message.timestamp
      assert_equal({ "call-1" => "2026-06-13T10:00:00.000Z" }, store.tool_call_timestamps(path, ["call-1", "missing-call"]))
      assert_equal({ "call-1" => { timestamp: "2026-06-13T10:00:00.000Z", prompt: "Review the diff" } }, store.subagent_tool_call_context(path, ["call-1", "missing-call"]))
    end
  end

  def test_formats_general_subagent_result_details
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        {
          type: "message",
          timestamp: "2026-07-10T19:42:00Z",
          message: {
            role: "toolResult",
            toolCallId: "call-1",
            toolName: "subagent",
            content: [{ type: "text", text: "Largest: `/home/vitek/.hermes` — **7.4G**." }],
            details: {
              task: "Find the largest directory",
              cwd: "/home/vitek",
              model: "openai-codex/gpt-5.6-sol",
              status: "done",
              tools: [
                {
                  id: "child-call-1",
                  name: "bash",
                  args: { command: "du -shx /home/vitek/.hermes", timeout: 120 },
                  status: "done",
                  output: "7.4G\t/home/vitek/.hermes\n"
                }
              ],
              textItems: ["Largest: `/home/vitek/.hermes` — **7.4G**."],
              streamingText: "",
              usage: {
                input: 6_523,
                output: 332,
                cacheRead: 1_536,
                cacheWrite: 0,
                cost: 0.043343,
                contextTokens: 2_854,
                turns: 3
              }
            },
            isError: false
          }
        }
      ])

      message = PiSessionStore.new(root: dir).messages(path).first

      assert_equal "subagent general", message.summary
      assert_equal "Find the largest directory", message.tool_prompt
      assert_equal <<~TEXT.chomp, message.text
        ✓ general
        ✓ $ du -shx /home/vitek/.hermes
          7.4G\t/home/vitek/.hermes

        Largest: `/home/vitek/.hermes` — **7.4G**.

        3 turns ↑6.5k ↓332 R1.5k $0.0433 ctx:2.9k openai-codex/gpt-5.6-sol
      TEXT
      assert_equal "subagent", message.tool_name
      assert message.tool_transcript
    end
  end

  def test_safely_formats_partial_general_subagent_details_and_unicode_arguments
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      unicode_value = "😀" * 101
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        {
          type: "message",
          timestamp: "2026-07-10T19:42:00Z",
          message: {
            role: "toolResult",
            toolCallId: "call-1",
            toolName: "subagent",
            content: [{ type: "text", text: "Still working" }],
            details: {
              tools: [
                nil,
                { name: "read", args: { path: "/tmp/file", offset: "Infinity", limit: {} }, output: nil },
                { name: "custom", args: { value: unicode_value }, output: nil }
              ],
              textItems: {},
              usage: { turns: "Infinity", input: {}, cost: "invalid" },
              model: "provider/model"
            },
            isError: false
          }
        }
      ])

      message = PiSessionStore.new(root: dir).messages(path).first

      assert message.text.valid_encoding?
      assert_includes message.text, "⏳ general"
      assert_includes message.text, "⏳ read /tmp/file"
      assert_includes message.text, "⏳ custom"
      assert_includes message.text, "Still working"
      assert_includes message.text, "provider/model"
    end
  end

  def test_safely_reads_non_finite_general_subagent_usage
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      File.write(path, [
        JSON.generate(type: "session", id: "session-1", cwd: "/tmp/project"),
        '{"type":"message","timestamp":"2026-07-10T19:42:00Z","message":{"role":"toolResult","toolCallId":"call-1","toolName":"subagent","content":[{"type":"text","text":"Done"}],"details":{"status":"done","tools":[],"textItems":["Done"],"usage":{"turns":1e10000}},"isError":false}}'
      ].join("\n") + "\n")

      message = PiSessionStore.new(root: dir).messages(path).first

      assert_equal "✓ general\n\nDone", message.text
      refute_respond_to message, :raw_details
    end
  end

  def test_keeps_generic_subagent_output_for_unknown_detail_shapes
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        {
          type: "message",
          timestamp: "2026-07-10T19:42:00Z",
          message: {
            role: "toolResult",
            toolCallId: "call-1",
            toolName: "subagent",
            content: [{ type: "text", text: "Unknown implementation result" }],
            details: { customProgress: 100 },
            isError: false
          }
        }
      ])

      message = PiSessionStore.new(root: dir).messages(path).first

      assert_equal "subagent", message.summary
      assert_equal "Unknown implementation result", message.text
      refute message.tool_transcript
    end
  end

  def test_rejects_non_subagent_and_invalid_tool_call_timestamps
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        {
          type: "message",
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "assistant",
            content: [{ type: "toolCall", id: "call-1", name: "custom_tool", arguments: {} }]
          }
        },
        {
          type: "message",
          timestamp: "10:00",
          message: {
            role: "assistant",
            content: [{ type: "toolCall", id: "call-2", name: "subagent", arguments: { agent: "reviewer", task: "Review the diff" } }]
          }
        },
        {
          type: "message",
          timestamp: "1e100",
          message: {
            role: "assistant",
            content: [{ type: "toolCall", id: "call-3", name: "subagent", arguments: { agent: "reviewer", task: "Review the diff" } }]
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:05:00Z",
          message: {
            role: "toolResult",
            toolCallId: "call-1",
            toolName: "subagent",
            content: [{ type: "text", text: "First result" }]
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:06:00Z",
          message: {
            role: "toolResult",
            toolCallId: "call-2",
            toolName: "subagent",
            content: [{ type: "text", text: "Second result" }]
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:07:00Z",
          message: {
            role: "toolResult",
            toolCallId: "call-3",
            toolName: "subagent",
            content: [{ type: "text", text: "Third result" }]
          }
        }
      ])

      store = PiSessionStore.new(root: dir)
      subagent_results = store.messages(path).select { |message| message.role == "toolResult" }

      assert_equal [Time.parse("2026-06-13T10:05:00Z"), Time.parse("2026-06-13T10:06:00Z"), Time.parse("2026-06-13T10:07:00Z")], subagent_results.map(&:timestamp)
      assert_empty store.tool_call_timestamps(path, ["call-1", "call-2", "call-3"])
      assert_empty store.tool_call_timestamps(File.join(dir, "missing.jsonl"), ["call-3"])
    end
  end

  def test_reads_structured_error_entries_as_error_messages
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        {
          type: "error",
          timestamp: "2026-06-13T10:00:00Z",
          error: {
            type: "invalid_request_error",
            message: "Third-party apps now draw from extra usage.",
            request_id: "req_123"
          }
        }
      ])

      messages = PiSessionStore.new(root: dir).messages(path)

      assert_equal 1, messages.length
      assert_equal "error", messages.first.role
      assert_equal "Third-party apps now draw from extra usage.", messages.first.text
      assert messages.first.error
      assert_equal Time.parse("2026-06-13T10:00:00Z"), messages.first.timestamp
    end
  end

  def test_reads_error_details_as_error_messages
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "error", timestamp: "2026-06-13T10:00:00Z", details: { message: "Nested provider failure" } }
      ])

      messages = PiSessionStore.new(root: dir).messages(path)

      assert_equal ["error"], messages.map(&:role)
      assert_equal ["Nested provider failure"], messages.map(&:text)
    end
  end

  def test_reads_final_error_entries_as_error_messages
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "agent_end", timestamp: "2026-06-13T10:00:00Z", finalError: { message: "Provider failed" } }
      ])

      messages = PiSessionStore.new(root: dir).messages(path)

      assert_equal ["error"], messages.map(&:role)
      assert_equal ["Provider failed"], messages.map(&:text)
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

  def test_reads_native_bash_execution_messages_in_full_and_indexed_history
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      entries = [
        { type: "session", id: "session-1", timestamp: "2026-06-13T10:00:00Z", cwd: "/tmp/project" },
        {
          type: "message", id: "bash-1", parentId: nil, timestamp: "2026-06-13T10:01:00Z",
          message: {
            role: "bashExecution", command: "printf included", output: "included output", exitCode: 7,
            cancelled: false, truncated: false, fullOutputPath: "/tmp/private-included.log", timestamp: 1
          }
        },
        {
          type: "message", id: "bash-2", parentId: "bash-1", timestamp: "2026-06-13T10:02:00Z",
          message: {
            role: "bashExecution", command: "printf excluded", output: "",
            cancelled: true, truncated: true, fullOutputPath: "/tmp/private-excluded.log", timestamp: 2,
            excludeFromContext: true
          }
        }
      ]
      write_jsonl(path, entries)
      store = PiSessionStore.new(root: dir)

      full_messages = store.messages(path)
      indexed_messages = store.conversation_window(path).messages

      assert_equal full_messages.map(&:to_h), indexed_messages.map(&:to_h)
      assert_equal 2, indexed_messages.length
      included, excluded = indexed_messages
      assert_equal "bashExecution", included.role
      assert_equal "included output", included.text
      assert_equal "$ printf included", included.summary
      assert_equal "bash", included.tool_name
      assert included.compact
      assert_equal 7, included.bash_exit_code
      refute included.bash_cancelled
      refute included.bash_truncated
      refute included.bash_excluded_from_context
      assert_equal Time.iso8601("2026-06-13T10:01:00Z"), included.timestamp
      assert_equal Time.at(0.001), included.bash_started_at
      assert_equal "$ printf excluded", excluded.summary
      assert_equal "", excluded.text
      assert excluded.bash_cancelled
      assert excluded.bash_truncated
      assert excluded.bash_excluded_from_context
      refute_includes indexed_messages.map(&:to_h).join, "private-included"
      refute_includes indexed_messages.map(&:to_h).join, "private-excluded"
    end
  end

  def test_bash_execution_context_estimates_include_command_and_output_but_not_double_bang_entries
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      summary = "Summary"
      command = "printf included"
      output = "included output"
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "message", id: "included", parentId: nil, message: { role: "bashExecution", command: command, output: output, exitCode: 0, cancelled: false, truncated: false, timestamp: 1 } },
        { type: "message", id: "excluded", parentId: "included", message: { role: "bashExecution", command: "secret command", output: "secret output", exitCode: 0, cancelled: false, truncated: false, timestamp: 2, excludeFromContext: true } },
        { type: "compaction", id: "compaction-1", parentId: "excluded", summary: summary, firstKeptEntryId: "included", tokensBefore: 100 }
      ])
      store = PiSessionStore.new(root: dir)
      expected_tokens = ([summary, command, output].join("\n").length / 4.0).ceil

      assert_equal expected_tokens, store.conversation(path).status.context_tokens
      assert_equal expected_tokens, store.conversation_window(path).status.context_tokens
    end
  end

  def test_large_native_bash_execution_uses_indexed_source_and_byte_estimates
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      output = "large-native-bash-output-#{"x" * 300_000}"
      entries = [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "message", id: "bash-1", parentId: nil, timestamp: "2026-06-13T10:00:00Z", message: { role: "bashExecution", command: "generate output", output: output, exitCode: 0, cancelled: false, truncated: false, timestamp: 1 } }
      ]
      parent_id = "bash-1"
      25.times do |index|
        id = "user-#{index}"
        entries << { type: "message", id: id, parentId: parent_id, message: { role: "user", content: [{ type: "text", text: "Message #{index}" }] } }
        parent_id = id
      end
      write_jsonl(path, entries)
      parsed_large_entry = false
      parse = JSON.method(:parse)
      window = nil

      replace_singleton_method(JSON, :parse, ->(line, *args) { parsed_large_entry = true if line.include?("large-native-bash-output-"); parse.call(line, *args) }) do
        window = PiSessionStore.new(root: dir).conversation_window(path)
      end

      assert_equal 26, window.total_message_count
      assert_equal 1, window.start_index
      assert_equal (0...25).map { |index| "Message #{index}" }, window.messages.map(&:text)
      refute parsed_large_entry
    end
  end

  def test_session_metadata_does_not_count_bash_execution_as_conversation_activity
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", timestamp: "2026-06-13T10:00:00Z", cwd: "/tmp/project" },
        { type: "message", timestamp: "2026-06-13T10:01:00Z", message: { role: "user", content: [{ type: "text", text: "Prompt" }] } },
        { type: "message", timestamp: "2026-06-13T10:02:00Z", message: { role: "bashExecution", command: "echo later", output: "later", exitCode: 0, cancelled: false, truncated: false, timestamp: 2 } }
      ])

      session = PiSessionStore.new(root: dir).sessions.first

      assert_equal 1, session.message_count
      assert_equal Time.iso8601("2026-06-13T10:01:00Z"), session.conversation_activity_at
      assert_nil session.latest_activity_kind
      assert_nil session.latest_activity_preview
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

  def test_displays_expanded_skill_prompts_as_commands
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      expanded_skill = <<~TEXT.chomp
        <skill name="diffx" location="/home/tester/.pi/agent/skills/diffx/SKILL.md">
        References are relative to /home/tester/.pi/agent/skills/diffx.

        # diffx

        Start a review.
        </skill>
      TEXT
      skill_directory = "/home/tester/.pi/agent/skills/diffx"
      invalid_skills = [
        expanded_skill.sub("relative to #{skill_directory}.", "relative to /tmp/other."),
        expanded_skill.gsub(skill_directory, "skills/diffx"),
        expanded_skill.gsub(skill_directory, "/home/tester/.pi/agent/skills/../diffx"),
        expanded_skill.gsub(skill_directory, "/home/tester/.pi/agent/skills/./diffx"),
        expanded_skill.gsub(skill_directory, "/home/tester/.pi/agent/skills//diffx"),
        expanded_skill.sub("location=\"#{skill_directory}/SKILL.md\"", "location=\"#{skill_directory}/\"")
      ]
      ordinary_xml = "<skill name=\"diffx\">not a Pi skill expansion</skill>"
      entries = [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "message", message: { role: "user", content: [{ type: "text", text: expanded_skill }] } },
        { type: "message", message: { role: "user", content: [{ type: "text", text: "#{expanded_skill}\n\ncomments" }] } }
      ]
      entries.concat(invalid_skills.map { |text| { type: "message", message: { role: "user", content: [{ type: "text", text: text }] } } })
      entries << { type: "message", message: { role: "user", content: [{ type: "text", text: ordinary_xml }] } }
      write_jsonl(path, entries)

      messages = PiSessionStore.new(root: dir).messages(path)

      assert_equal ["/skill:diffx", "/skill:diffx comments", *invalid_skills, ordinary_xml], messages.map(&:text)
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

  def test_preserves_context_usage_after_aborted_and_failed_responses
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "message", message: { role: "assistant", content: [{ type: "text", text: "Earlier" }], usage: { totalTokens: 12_345, contextWindow: 200_000 } } },
        { type: "message", message: { role: "assistant", stopReason: "aborted", content: [], usage: { totalTokens: 20_000 } } },
        { type: "message", message: { role: "assistant", stopReason: "error", content: [], usage: { totalTokens: 30_000 } } }
      ])

      status = PiSessionStore.new(root: dir).status(path)

      assert_equal 12_345, status.context_tokens
      assert_equal 200_000, status.context_limit
    end
  end

  def test_infers_model_from_assistant_messages_when_change_entries_are_missing
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "message", message: { role: "assistant", provider: "anthropic", model: "claude-sonnet-4", content: [] } }
      ])

      status = PiSessionStore.new(root: dir).status(path)

      assert_equal "anthropic", status.provider
      assert_equal "claude-sonnet-4", status.model_id
    end
  end

  def test_indexed_conversation_window_matches_full_projection_with_tool_pairing
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        {
          type: "message",
          id: "assistant-1",
          parentId: nil,
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "assistant",
            content: [
              { type: "thinking", thinking: "Inspecting" },
              { type: "text", text: "Running it" },
              { type: "toolCall", id: "call-1", name: "bash", arguments: { command: "echo hi" } }
            ]
          }
        },
        {
          type: "message",
          id: "result-1",
          parentId: "assistant-1",
          timestamp: "2026-06-13T10:00:01Z",
          message: {
            role: "toolResult",
            toolCallId: "call-1",
            toolName: "bash",
            content: [{ type: "text", text: "hi" }],
            isError: false
          }
        },
        { type: "message", id: "user-1", parentId: "result-1", message: { role: "user", content: [{ type: "text", text: "Thanks" }] } }
      ])
      store = PiSessionStore.new(root: dir)

      full_messages = store.messages(path)
      window = store.conversation_window(path)

      assert_equal full_messages.map(&:to_h), window.messages.map(&:to_h)
      assert_equal full_messages.length, window.total_message_count
      assert_equal 0, window.start_index
    end
  end

  def test_indexes_a_large_assistant_tool_call_without_parsing_it_outside_the_window
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      sentinel = "large-assistant-arguments-#{"x" * 300_000}"
      entries = [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        {
          type: "message",
          id: "assistant-1",
          parentId: nil,
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "assistant",
            content: [
              { type: "thinking", thinking: "Opaque reasoning", thinkingSignature: "encrypted", redacted: true },
              { type: "toolCall", id: "call-1", name: "inspect", arguments: { payload: sentinel }, thoughtSignature: "opaque-signature" }
            ],
            api: "responses",
            provider: "openai-codex",
            model: "gpt-5.5",
            usage: { totalTokens: 100 },
            stopReason: "toolUse",
            timestamp: 1_781_341_200_000,
            responseModel: "gpt-5.5-2026-06-13"
          }
        }
      ]
      parent_id = "assistant-1"
      25.times do |index|
        id = "user-#{index}"
        entries << { type: "message", id: id, parentId: parent_id, timestamp: "2026-06-13T10:01:00Z", message: { role: "user", content: [{ type: "text", text: "Message #{index}" }], timestamp: 1_781_341_260_000 } }
        parent_id = id
      end
      entries << { type: "compaction", id: "compaction-1", parentId: parent_id, timestamp: "2026-06-13T10:02:00Z", summary: "Compacted", firstKeptEntryId: "assistant-1", tokensBefore: 100 }
      write_jsonl(path, entries)
      parsed_lines = []
      parse = JSON.method(:parse)
      window = nil

      replace_singleton_method(JSON, :parse, ->(line, *args) { parsed_lines << line; parse.call(line, *args) }) do
        window = PiSessionStore.new(root: dir).conversation_window(path)
      end

      refute_nil window
      assert_equal 28, window.total_message_count
      assert_equal 2, window.start_index
      assert_equal [*(0...25).map { |index| "Message #{index}" }, "Compacted"], window.messages.map(&:text)
      assert_nil window.status.context_tokens
      refute parsed_lines.any? { |line| line.include?("large-assistant-arguments-") }
    end
  end

  def test_indexes_large_unicode_thinking_when_capture_ends_mid_character
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      entries = [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "message", id: "assistant-1", parentId: nil, timestamp: "2026-06-13T10:00:00Z", message: { role: "assistant", content: [{ type: "thinking", thinking: "😀" * 80_000 }] } }
      ]
      parent_id = "assistant-1"
      25.times do |index|
        id = "user-#{index}"
        entries << { type: "message", id: id, parentId: parent_id, message: { role: "user", content: [{ type: "text", text: "Message #{index}" }] } }
        parent_id = id
      end
      write_jsonl(path, entries)

      window = PiSessionStore.new(root: dir).conversation_window(path)

      assert_equal 26, window.total_message_count
      assert_equal 1, window.start_index
      assert_equal (0...25).map { |index| "Message #{index}" }, window.messages.map(&:text)
    end
  end

  def test_indexes_large_assistant_thinking_with_decoded_control_whitespace
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        {
          type: "message",
          id: "assistant-1",
          parentId: nil,
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "assistant",
            content: [{ type: "thinking", thinking: "**Heading**\n\n\n\t", thinkingSignature: "x" * 300_000 }]
          }
        }
      ])
      store = PiSessionStore.new(root: dir)

      assert_equal store.messages(path).map(&:to_h), store.conversation_window(path).messages.map(&:to_h)
    end
  end

  def test_does_not_parse_a_large_bash_command_outside_the_window
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      command = "large-bash-command-#{"x" * 300_000}"
      entries = [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        {
          type: "message", id: "assistant-1", parentId: nil, timestamp: "2026-06-13T10:00:00Z",
          message: { role: "assistant", content: [{ type: "toolCall", id: "call-1", name: "bash", arguments: { command: command } }] }
        }
      ]
      parent_id = "assistant-1"
      25.times do |index|
        id = "user-#{index}"
        entries << { type: "message", id: id, parentId: parent_id, message: { role: "user", content: [{ type: "text", text: "Message #{index}" }] } }
        parent_id = id
      end
      write_jsonl(path, entries)
      parsed_large_entry = false
      parse = JSON.method(:parse)

      replace_singleton_method(JSON, :parse, ->(line, *args) { parsed_large_entry = true if line.include?("large-bash-command-"); parse.call(line, *args) }) do
        assert_equal 1, PiSessionStore.new(root: dir).conversation_window(path).start_index
      end
      refute parsed_large_entry
    end
  end

  def test_does_not_parse_a_large_whitespace_compaction_outside_the_window
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      entries = [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "compaction", id: "compaction-1", parentId: nil, timestamp: "2026-06-13T10:00:00Z", summary: " " * 300_000, firstKeptEntryId: "", tokensBefore: 100 }
      ]
      parent_id = "compaction-1"
      25.times do |index|
        id = "user-#{index}"
        entries << { type: "message", id: id, parentId: parent_id, message: { role: "user", content: [{ type: "text", text: "Message #{index}" }] } }
        parent_id = id
      end
      write_jsonl(path, entries)
      parsed_large_entry = false
      parse = JSON.method(:parse)

      replace_singleton_method(JSON, :parse, ->(line, *args) { parsed_large_entry = true if line.bytesize > 300_000; parse.call(line, *args) }) do
        assert_equal 1, PiSessionStore.new(root: dir).conversation_window(path).start_index
      end
      refute parsed_large_entry
    end
  end

  def test_does_not_parse_a_large_general_subagent_result_outside_the_window
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      sentinel = "large-subagent-output-#{"x" * 1_048_576}"
      entries = [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        {
          type: "message",
          id: "result-1",
          parentId: nil,
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "toolResult",
            toolCallId: "call-1",
            toolName: "subagent",
            content: [{ type: "text", text: "Done" }],
            details: {
              task: "Review",
              status: "done",
              tools: [{ name: "bash", status: "done", args: { command: sentinel }, output: "Reviewed" }],
              textItems: [],
              streamingText: "Done",
              usage: {}
            },
            isError: false,
            timestamp: 1_781_341_200_000
          }
        }
      ]
      parent_id = "result-1"
      25.times do |index|
        id = "user-#{index}"
        entries << { type: "message", id: id, parentId: parent_id, timestamp: "2026-06-13T10:01:00Z", message: { role: "user", content: [{ type: "text", text: "Message #{index}" }], timestamp: 1_781_341_260_000 } }
        parent_id = id
      end
      write_jsonl(path, entries)
      parsed_lines = []
      parse = JSON.method(:parse)
      window = nil

      replace_singleton_method(JSON, :parse, ->(line, *args) { parsed_lines << line; parse.call(line, *args) }) do
        window = PiSessionStore.new(root: dir).conversation_window(path)
      end

      refute_nil window
      assert_equal 26, window.total_message_count
      assert_equal 1, window.start_index
      refute parsed_lines.any? { |line| line.include?("large-subagent-output-") }
    end
  end

  def test_indexes_large_general_subagent_detail_values_without_using_them_for_preflight
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        {
          type: "message",
          id: "result-1",
          parentId: nil,
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "toolResult",
            toolCallId: "call-1",
            toolName: "subagent",
            content: [{ type: "text", text: "Done" }],
            details: { tools: [], textItems: ["x" * 300_000], streamingText: {}, usage: {} },
            isError: false,
            timestamp: 1_781_341_200_000
          }
        }
      ])
      store = PiSessionStore.new(root: dir)

      assert_equal store.messages(path).map(&:to_h), store.conversation_window(path).messages.map(&:to_h)
    end
  end

  def test_large_empty_tool_result_does_not_add_a_rendered_cursor
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      entries = [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        {
          type: "message",
          id: "result-1",
          parentId: nil,
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "toolResult",
            toolCallId: "call-1",
            toolName: "inspect",
            content: [{ type: "text", text: "" }],
            details: { ignored: "x" * 300_000 },
            isError: false
          }
        }
      ]
      parent_id = "result-1"
      25.times do |index|
        id = "user-#{index}"
        entries << { type: "message", id: id, parentId: parent_id, message: { role: "user", content: [{ type: "text", text: "Message #{index}" }] } }
        parent_id = id
      end
      write_jsonl(path, entries)
      store = PiSessionStore.new(root: dir)

      assert_equal store.messages(path, current_leaf_id: parent_id).map(&:to_h), store.conversation_window(path).messages.map(&:to_h)
      assert_equal 25, store.conversation_window(path).total_message_count
    end
  end

  def test_does_not_parse_a_large_tool_result_outside_the_window
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      sentinel = "large-tool-output-#{" " * 300_000}"
      entries = [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        {
          type: "message",
          id: "assistant-1",
          parentId: nil,
          timestamp: "2026-06-13T09:59:00Z",
          message: {
            role: "assistant",
            content: [{ type: "toolCall", id: "call-1", name: "read", arguments: { path: "large.txt" } }],
            api: "responses",
            provider: "openai-codex",
            model: "gpt-5.5",
            usage: { totalTokens: 100 },
            stopReason: "toolUse",
            timestamp: 1_781_341_140_000
          }
        },
        {
          type: "message",
          id: "result-1",
          parentId: "assistant-1",
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "toolResult",
            toolCallId: "call-1",
            toolName: "read",
            content: [{ type: "text", text: sentinel }],
            addedToolNames: ["inspect"],
            isError: true,
            timestamp: 1_781_341_200_000
          }
        }
      ]
      parent_id = "result-1"
      25.times do |index|
        id = "user-#{index}"
        entries << { type: "message", id: id, parentId: parent_id, timestamp: "2026-06-13T10:01:00Z", message: { role: "user", content: [{ type: "text", text: "Message #{index}" }], timestamp: 1_781_341_260_000 } }
        parent_id = id
      end
      write_jsonl(path, entries)
      parsed_lines = []
      parse = JSON.method(:parse)

      replace_singleton_method(JSON, :parse, ->(line, *args) { parsed_lines << line; parse.call(line, *args) }) do
        window = PiSessionStore.new(root: dir).conversation_window(path)
        assert_equal 1, window.start_index
      end

      refute parsed_lines.any? { |line| line.include?("large-tool-output-") }
    end
  end

  def test_includes_a_large_bash_result_when_only_ignored_details_are_large
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      ignored_details = "ignored-bash-details-#{"x" * 1_048_576}"
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        {
          type: "message",
          id: "assistant-1",
          parentId: nil,
          timestamp: "2026-06-13T09:59:00Z",
          message: {
            role: "assistant",
            content: [{ type: "toolCall", id: "call-1", name: "bash", arguments: { command: "echo ok" } }],
            api: "responses",
            provider: "openai-codex",
            model: "gpt-5.5",
            usage: { totalTokens: 100 },
            stopReason: "toolUse",
            timestamp: 1_781_341_140_000
          }
        },
        {
          type: "message",
          id: "result-1",
          parentId: "assistant-1",
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "toolResult",
            toolCallId: "call-1",
            toolName: "bash",
            content: [{ type: "text", text: "ok" }],
            details: { ignored: ignored_details },
            isError: false,
            timestamp: 1_781_341_200_000
          }
        },
        { type: "message", id: "user-1", parentId: "result-1", timestamp: "2026-06-13T10:01:00Z", message: { role: "user", content: [{ type: "text", text: "Continue" }], timestamp: 1_781_341_260_000 } }
      ])

      window = PiSessionStore.new(root: dir).conversation_window(path)

      refute_nil window
      assert_equal 0, window.start_index
      assert_equal ["ok", "Continue"], window.messages.map(&:text)
    end
  end

  def test_indexes_large_canonical_compaction_branch_summary_and_custom_message_entries
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      ignored_details = { payload: "x" * 300_000 }
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "compaction", id: "compaction-1", parentId: nil, timestamp: "2026-06-13T10:00:00Z", summary: "Compacted", firstKeptEntryId: "", tokensBefore: 100, details: ignored_details, fromHook: false },
        { type: "branch_summary", id: "summary-1", parentId: "compaction-1", timestamp: "2026-06-13T10:01:00Z", fromId: "compaction-1", summary: "Branch summary", details: ignored_details, fromHook: false },
        { type: "custom_message", customType: "notice", content: "Visible notice", display: true, details: ignored_details, id: "custom-1", parentId: "summary-1", timestamp: "2026-06-13T10:02:00Z" }
      ])

      store = PiSessionStore.new(root: dir)
      window = store.conversation_window(path)

      refute_nil window
      assert_equal store.messages(path).map(&:to_h), window.messages.map(&:to_h)
      assert_equal ["Compacted", "Visible notice"], window.messages.map(&:text)
    end
  end

  def test_indexed_subagent_result_loads_prompt_and_timestamp_from_its_hidden_call
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      entries = [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        {
          type: "message",
          id: "call-entry",
          parentId: nil,
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "assistant",
            content: [{ type: "toolCall", id: "call-1", name: "subagent", arguments: { task: "Review the change" } }]
          }
        }
      ]
      parent_id = "call-entry"
      25.times do |index|
        id = "user-#{index}"
        entries << { type: "message", id: id, parentId: parent_id, message: { role: "user", content: [{ type: "text", text: "Message #{index}" }] } }
        parent_id = id
      end
      entries << {
        type: "message",
        id: "result-entry",
        parentId: parent_id,
        timestamp: "2026-06-13T10:05:00Z",
        message: {
          role: "toolResult",
          toolCallId: "call-1",
          toolName: "subagent",
          content: [{ type: "text", text: "No findings" }]
        }
      }
      write_jsonl(path, entries)

      result = PiSessionStore.new(root: dir).conversation_window(path).messages.last

      assert_equal "No findings", result.text
      assert_equal "Review the change", result.tool_prompt
      assert_equal Time.iso8601("2026-06-13T10:00:00Z"), result.timestamp
    end
  end

  def test_indexed_conversation_window_is_invalidated_after_append
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      entries = [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "message", id: "user-1", parentId: nil, message: { role: "user", content: [{ type: "text", text: "First" }] } }
      ]
      File.write(path, entries.map { |entry| JSON.generate(entry) }.join("\n") + "\n")
      store = PiSessionStore.new(root: dir)
      assert_equal 1, store.conversation_window(path).total_message_count

      File.open(path, "a") do |file|
        file.puts JSON.generate(type: "message", id: "user-2", parentId: "user-1", message: { role: "user", content: [{ type: "text", text: "Second" }] })
      end
      parsed_lines = []
      parse = JSON.method(:parse)
      window = nil
      replace_singleton_method(JSON, :parse, ->(line, *args) { parsed_lines << line; parse.call(line, *args) }) do
        window = store.conversation_window(path)
      end

      assert_equal 2, window.total_message_count
      assert_equal ["First", "Second"], window.messages.map(&:text)
      assert_equal 3, parsed_lines.length
      assert_equal 2, parsed_lines.count { |line| line.include?('"id":"user-2"') }
      assert_equal 1, parsed_lines.count { |line| line.include?('"id":"user-1"') }
    end
  end

  def test_indexed_conversation_rebuilds_after_an_in_place_rewrite_and_append
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      initial_entries = [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "message", id: "entry-a", parentId: nil, message: { role: "user", content: [{ type: "text", text: "First" }] } },
        { type: "message", id: "entry-b", parentId: "entry-a", message: { role: "user", content: [{ type: "text", text: "Second" }] } }
      ]
      File.write(path, initial_entries.map { |entry| JSON.generate(entry) }.join("\n") + "\n")
      store = PiSessionStore.new(root: dir)
      assert_equal ["First", "Second"], store.conversation_window(path).messages.map(&:text)

      rewritten_entries = [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "message", id: "entry-x", parentId: nil, message: { role: "user", content: [{ type: "text", text: "One" }] } },
        { type: "message", id: "entry-y", parentId: "entry-x", message: { role: "user", content: [{ type: "text", text: "Two" }] } },
        { type: "message", id: "entry-z", parentId: "entry-y", message: { role: "user", content: [{ type: "text", text: "Three" }] } }
      ]
      File.write(path, rewritten_entries.map { |entry| JSON.generate(entry) }.join("\n") + "\n")

      window = store.conversation_window(path, current_leaf_id: "entry-z", current_leaf_supplied: true)
      assert_equal ["One", "Two", "Three"], window.messages.map(&:text)
    end
  end

  def test_indexed_conversation_defaults_to_the_latest_persisted_branch
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "message", id: "user-1", parentId: nil, message: { role: "user", content: [{ type: "text", text: "Root" }] } },
        { type: "message", id: "assistant-1", parentId: "user-1", message: { role: "assistant", content: [{ type: "text", text: "Root answer" }] } },
        { type: "message", id: "old-user", parentId: "assistant-1", message: { role: "user", content: [{ type: "text", text: "Old branch" }] } },
        { type: "message", id: "old-answer", parentId: "old-user", message: { role: "assistant", content: [{ type: "text", text: "Old answer" }] } },
        { type: "message", id: "latest-user", parentId: "assistant-1", message: { role: "user", content: [{ type: "text", text: "Latest branch" }] } }
      ])

      window = PiSessionStore.new(root: dir).conversation_window(path)

      assert_equal ["Root", "Root answer", "Latest branch"], window.messages.map(&:text)
      assert_equal "latest-user", window.tree_leaf_id
      assert_equal 3, window.total_message_count
    end
  end

  def test_indexed_conversation_distinguishes_an_explicit_root_leaf
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "message", id: "user-1", parentId: nil, message: { role: "user", content: [{ type: "text", text: "First" }] } },
        { type: "message", id: "assistant-1", parentId: "user-1", message: { role: "assistant", content: [{ type: "text", text: "Answer" }] } },
        { type: "leaf", id: "leaf-1", parentId: "assistant-1", targetId: nil }
      ])
      store = PiSessionStore.new(root: dir)

      assert_equal 2, store.conversation_window(path, current_leaf_id: "assistant-1").total_message_count
      assert_equal 0, store.conversation_window(path).total_message_count
      root_window = store.conversation_window(path, current_leaf_id: nil, current_leaf_supplied: true)
      assert_equal 0, root_window.total_message_count
      assert_empty root_window.messages
      assert_nil root_window.latest_stable_tree_position_id
    end
  end

  def test_indexed_conversation_falls_back_for_unknown_large_custom_message_content
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "custom_message", customType: "notice", content: { text: "x" * 300_000 }, display: true, id: "custom-1", parentId: nil, timestamp: "2026-06-13T10:00:00Z" }
      ])

      assert_nil PiSessionStore.new(root: dir).conversation_window(path)

      scalar_path = File.join(dir, "scalar-content.jsonl")
      write_jsonl(scalar_path, [
        { type: "session", id: "session-2", cwd: "/tmp/project" },
        { type: "custom_message", customType: "notice", content: [{ type: "text", text: "x" * 300_000 }, 123], display: true, id: "custom-2", parentId: nil, timestamp: "2026-06-13T10:00:00Z" }
      ])
      assert_nil PiSessionStore.new(root: dir).conversation_window(scalar_path)
    end
  end

  def test_indexed_conversation_falls_back_for_truncated_semantic_identifiers
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      tool_call_id = "call-#{"x" * 9_000}"
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "message", id: "assistant-1", parentId: nil, timestamp: "2026-06-13T10:00:00Z", message: { role: "assistant", content: [{ type: "toolCall", id: tool_call_id, name: "bash", arguments: { command: "large output" } }] } },
        { type: "message", id: "result-1", parentId: "assistant-1", timestamp: "2026-06-13T10:00:01Z", message: { role: "toolResult", toolCallId: tool_call_id, toolName: "bash", content: [{ type: "text", text: "x" * 300_000 }], isError: false } }
      ])

      assert_nil PiSessionStore.new(root: dir).conversation_window(path)

      branch_path = File.join(dir, "branch-summary.jsonl")
      write_jsonl(branch_path, [
        { type: "session", id: "session-2", cwd: "/tmp/project" },
        { type: "branch_summary", id: "summary-1", parentId: nil, timestamp: "2026-06-13T10:00:00Z", fromId: "entry-#{"x" * 9_000}", summary: "x" * 300_000 }
      ])
      assert_nil PiSessionStore.new(root: dir).conversation_window(branch_path)
    end
  end

  def test_indexed_conversation_falls_back_for_malformed_large_json
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      malformed = JSON.generate(
        type: "custom_message",
        customType: "notice",
        content: "Visible notice",
        display: true,
        details: { ignored: "x" * 300_000 },
        id: "custom-1",
        parentId: nil,
        timestamp: "2026-06-13T10:00:00Z"
      ).sub(/\}\z/, ",}")
      File.write(path, [JSON.generate(type: "session", id: "session-1", cwd: "/tmp/project"), malformed].join("\n"))

      assert_nil PiSessionStore.new(root: dir).conversation_window(path)
    end
  end

  def test_indexed_conversation_falls_back_for_an_unfamiliar_large_entry_layout
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      File.write(path, [
        JSON.generate(type: "session", id: "session-1", cwd: "/tmp/project"),
        JSON.generate(message: { content: [{ type: "text", text: "x" * 300_000 }], role: "user" }, timestamp: "2026-06-13T10:00:00Z", parentId: nil, id: "user-1", type: "message")
      ].join("\n") + "\n")

      assert_nil PiSessionStore.new(root: dir).conversation_window(path)
    end
  end

  def test_file_snapshot_reports_file_identity_and_last_append_cursor
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "message", id: "entry-1", parentId: nil, message: { role: "user", content: [] } },
        { type: "session_info", id: "entry-2", parentId: "entry-1", name: "Renamed" }
      ])

      snapshot = PiSessionStore.new(root: dir).file_snapshot(path)

      assert_equal "entry-2", snapshot.append_cursor
      assert_equal "entry-2", snapshot.persisted_leaf_id
      assert_operator snapshot.size, :>, 0
      assert_operator snapshot.mtime_ns, :>, 0
      assert snapshot.revision.is_a?(String)
    end
  end

  def test_reads_appended_entry_ids_between_stable_snapshots
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "message", id: "entry-1", parentId: nil, message: { role: "user", content: [] } }
      ])
      store = PiSessionStore.new(root: dir)
      before = store.file_snapshot(path)
      File.open(path, "a") do |file|
        file.puts(JSON.generate(type: "message", id: "entry-2", parentId: "entry-1", message: { role: "assistant", content: [] }))
        file.puts(JSON.generate(type: "message", id: "entry-3", parentId: "entry-2", message: { role: "user", content: [] }))
      end
      after = store.file_snapshot(path)

      assert_equal ["entry-2", "entry-3"], store.appended_entry_ids(path, before, after)
    end
  end

  def test_file_snapshot_handles_a_tree_entry_larger_than_its_read_chunk
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "message", id: "large-entry", parentId: nil, message: { role: "user", content: [{ type: "text", text: "x" * 40_000 }] } }
      ])

      snapshot = PiSessionStore.new(root: dir).file_snapshot(path)

      assert_equal "large-entry", snapshot.append_cursor
      assert snapshot.complete
    end
  end

  def test_file_snapshot_ignores_incomplete_final_jsonl_entry
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      write_jsonl(path, [
        { type: "session", id: "session-1", cwd: "/tmp/project" },
        { type: "message", id: "entry-1", parentId: nil, message: { role: "user", content: [] } }
      ])
      File.open(path, "a") { |file| file.write("\n{\"type\":\"message\",\"id\":") }

      snapshot = PiSessionStore.new(root: dir).file_snapshot(path)

      assert_equal "entry-1", snapshot.append_cursor
      refute snapshot.complete
    end
  end

  private

  def concurrent_session_reads(store)
    read_count = 0
    count_mutex = Mutex.new
    original = File.method(:foreach)
    foreach = lambda do |*args, **kwargs, &block|
      count_mutex.synchronize { read_count += 1 }
      sleep 0.05
      original.call(*args, **kwargs, &block)
    end
    ready = Queue.new
    start = Queue.new
    results = nil

    replace_singleton_method(File, :foreach, foreach) do
      threads = 2.times.map do
        Thread.new do
          ready << true
          start.pop
          store.sessions
        end
      end
      2.times { ready.pop }
      2.times { start << true }
      results = threads.map(&:value)
    end

    [read_count, results]
  end

  def replace_singleton_method(receiver, name, replacement)
    original = receiver.method(name)
    receiver.singleton_class.define_method(name, replacement)
    yield
  ensure
    receiver.singleton_class.define_method(name, original)
  end

  def write_jsonl(path, entries)
    File.write(path, entries.map { |entry| JSON.generate(entry) }.join("\n"))
  end
end
