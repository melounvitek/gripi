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

  def write_jsonl(path, entries)
    File.write(path, entries.map { |entry| JSON.generate(entry) }.join("\n"))
  end
end
