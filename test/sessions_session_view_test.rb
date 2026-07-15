ENV["GRIPI_ADMIN_PASSWORD"] ||= "test-password"

require "minitest/autorun"
require "tmpdir"
require "fileutils"
require "json"
require_relative "../lib/sessions/session_view"
require_relative "../lib/sessions/sidebar"
require_relative "../lib/pi_session_store"
require_relative "../lib/gateway_read_state_store"
require_relative "../lib/pi_attachment_store"

class SessionsSessionViewTest < Minitest::Test
  def test_builds_existing_session_view_instance_variables
    Dir.mktmpdir do |dir|
      session_path = write_session(dir)
      read_state = GatewayReadStateStore.new(path: File.join(dir, "read-state.json"))

      view = Sessions::SessionView.build(
        sessions_root: dir,
        params: { "session" => session_path },
        include_conversation: true,
        read_state_store: read_state,
        attachment_store: PiAttachmentStore.new(root: File.join(dir, "attachments")),
        rpc_clients: inactive_rpc_clients,
        mark_selected_read: true
      )

      assignments = view.to_instance_variables
      assert_instance_of PiSessionStore, assignments.fetch(:@store)
      assert_equal [session_path], assignments.fetch(:@groups).values.flatten.map(&:path)
      assert_equal session_path, assignments.fetch(:@selected_session).path
      assert_instance_of Sessions::Sidebar, assignments.fetch(:@sidebar)
      assert_equal ["Hello"], assignments.fetch(:@messages).map(&:text)
      refute assignments.fetch(:@conversation_has_older_messages)
      assert_equal 0, assignments.fetch(:@conversation_older_message_count)
      assert_equal({}, assignments.fetch(:@attachment_counts))
      assert_instance_of PiSessionStore::Status, assignments.fetch(:@session_status)
      refute assignments.fetch(:@current_tree_leaf_known)
      refute assignments.fetch(:@viewing_older_tree_leaf)
    end
  end

  def test_explicit_empty_selection_does_not_default_to_the_most_recent_session
    Dir.mktmpdir do |dir|
      write_session(dir)

      view = Sessions::SessionView.build(
        sessions_root: dir,
        params: { "no_session" => "1" },
        include_conversation: true,
        read_state_store: GatewayReadStateStore.new(path: File.join(dir, "read-state.json")),
        attachment_store: PiAttachmentStore.new(root: File.join(dir, "attachments")),
        rpc_clients: inactive_rpc_clients,
        mark_selected_read: true
      )

      assert_nil view.selected_session
      assert_empty view.messages
    end
  end

  def test_fallback_selects_the_most_recent_other_session_across_projects
    Dir.mktmpdir do |dir|
      project_a = File.join(dir, "project-a")
      project_b = File.join(dir, "project-b")
      FileUtils.mkdir_p([project_a, project_b])
      detached_path = write_session(dir, "detached", cwd: project_a, timestamp: "2026-06-13T10:03:00Z")
      write_session(dir, "same-project-older", cwd: project_a, timestamp: "2026-06-13T10:01:00Z")
      expected_path = write_session(dir, "other-project-newer", cwd: project_b, timestamp: "2026-06-13T10:02:00Z")

      view = Sessions::SessionView.build(
        sessions_root: dir,
        params: { "session_fallback_excluding" => detached_path },
        include_conversation: true,
        read_state_store: GatewayReadStateStore.new(path: File.join(dir, "read-state.json")),
        attachment_store: PiAttachmentStore.new(root: File.join(dir, "attachments")),
        rpc_clients: inactive_rpc_clients,
        mark_selected_read: true
      )

      assert_equal expected_path, view.selected_session.path
    end
  end

  def test_non_conversation_view_does_not_prepare_transcript_details
    Dir.mktmpdir do |dir|
      session_path = write_session(dir)

      view = Sessions::SessionView.build(
        sessions_root: dir,
        params: { "session" => session_path },
        include_conversation: false,
        read_state_store: GatewayReadStateStore.new(path: File.join(dir, "read-state.json")),
        attachment_store: PiAttachmentStore.new(root: File.join(dir, "attachments")),
        rpc_clients: inactive_rpc_clients,
        mark_selected_read: false
      )

      assignments = view.to_instance_variables
      assert_equal session_path, assignments.fetch(:@selected_session).path
      assert_empty assignments.fetch(:@messages)
      assert_nil assignments.fetch(:@latest_tree_leaf_id)
      assert_nil assignments.fetch(:@session_status)
      refute assignments.fetch(:@conversation_has_older_messages)
    end
  end

  def test_conversation_uses_latest_window_for_long_history
    Dir.mktmpdir do |dir|
      session_path = write_session_with_messages(dir, 180)

      view = Sessions::SessionView.build(
        sessions_root: dir,
        params: { "session" => session_path },
        include_conversation: true,
        read_state_store: GatewayReadStateStore.new(path: File.join(dir, "read-state.json")),
        attachment_store: PiAttachmentStore.new(root: File.join(dir, "attachments")),
        rpc_clients: inactive_rpc_clients,
        mark_selected_read: false
      )

      assignments = view.to_instance_variables
      rendered_text = assignments.fetch(:@messages).map(&:text)
      assert_equal 150, rendered_text.length
      refute_includes rendered_text, "Message 30"
      assert_equal "Message 31", rendered_text.first
      assert_equal "Message 180", rendered_text.last
      assert assignments.fetch(:@conversation_has_older_messages)
      assert_equal 30, assignments.fetch(:@conversation_older_message_count)
      assert_equal 30, assignments.fetch(:@conversation_start_index)
    end
  end

  def test_builds_older_conversation_window_before_cursor
    Dir.mktmpdir do |dir|
      session_path = write_session_with_messages(dir, 220)

      view = Sessions::SessionView.older_window(
        sessions_root: dir,
        session_path: session_path,
        cursor: 70,
        current_leaf_id: nil,
        attachment_store: PiAttachmentStore.new(root: File.join(dir, "attachments"))
      )

      assert_equal (1..70).map { |index| "Message #{index}" }, view.fetch(:messages).map(&:text)
      assert_equal 0, view.fetch(:next_cursor)
      refute view.fetch(:has_older_messages)
      assert_equal 0, view.fetch(:older_message_count)
      assert_equal({}, view.fetch(:attachment_counts))
    end
  end

  def test_older_conversation_window_stays_on_the_requested_tree_leaf
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      entries = [
        { type: "session", id: "session", cwd: dir },
        { type: "message", id: "user-1", parentId: nil, message: { role: "user", content: [{ type: "text", text: "Root" }] } },
        { type: "message", id: "assistant-1", parentId: "user-1", message: { role: "assistant", content: [{ type: "text", text: "Root answer" }] } },
        { type: "message", id: "user-2", parentId: "assistant-1", message: { role: "user", content: [{ type: "text", text: "Selected branch" }] } },
        { type: "message", id: "assistant-2", parentId: "user-2", message: { role: "assistant", content: [{ type: "text", text: "Selected answer" }] } },
        { type: "message", id: "user-3", parentId: "assistant-1", message: { role: "user", content: [{ type: "text", text: "Other branch" }] } }
      ]
      File.write(path, entries.map { |entry| JSON.generate(entry) }.join("\n") + "\n")

      view = Sessions::SessionView.older_window(
        sessions_root: dir,
        session_path: path,
        cursor: 4,
        current_leaf_id: "assistant-2",
        attachment_store: PiAttachmentStore.new(root: File.join(dir, "attachments"))
      )

      assert_equal ["Root", "Root answer", "Selected branch", "Selected answer"], view.fetch(:messages).map(&:text)
    end
  end

  def test_conversation_renders_all_messages_when_within_window
    Dir.mktmpdir do |dir|
      session_path = write_session_with_messages(dir, 40)

      view = Sessions::SessionView.build(
        sessions_root: dir,
        params: { "session" => session_path },
        include_conversation: true,
        read_state_store: GatewayReadStateStore.new(path: File.join(dir, "read-state.json")),
        attachment_store: PiAttachmentStore.new(root: File.join(dir, "attachments")),
        rpc_clients: inactive_rpc_clients,
        mark_selected_read: false
      )

      assignments = view.to_instance_variables
      assert_equal (1..40).map { |index| "Message #{index}" }, assignments.fetch(:@messages).map(&:text)
      refute assignments.fetch(:@conversation_has_older_messages)
      assert_equal 0, assignments.fetch(:@conversation_older_message_count)
    end
  end

  def test_pending_session_uses_current_time_for_conversation_activity
    Dir.mktmpdir do |dir|
      pending_path = File.join(dir, "pending.jsonl")
      now = Time.iso8601("2026-06-13T10:00:00Z")

      view = Sessions::SessionView.build(
        sessions_root: dir,
        params: { "session" => pending_path },
        include_conversation: false,
        read_state_store: GatewayReadStateStore.new(path: File.join(dir, "read-state.json")),
        attachment_store: PiAttachmentStore.new(root: File.join(dir, "attachments")),
        rpc_clients: inactive_rpc_clients,
        mark_selected_read: false,
        pending_sessions: [[pending_path, dir]],
        now: now
      )

      assert_equal now, view.selected_session.conversation_activity_at
    end
  end

  def test_hidden_persisted_session_is_not_reintroduced_from_pending_sessions
    Dir.mktmpdir do |dir|
      missing_cwd = File.join(dir, "missing-project")
      session_path = File.join(dir, "session.jsonl")
      File.write(session_path, JSON.generate({ type: "session", id: "session", cwd: missing_cwd }) + "\n")
      active_rpc_clients = Class.new do
        def active?(_path)
          true
        end
      end.new

      view = Sessions::SessionView.build(
        sessions_root: dir,
        params: { "session" => session_path },
        include_conversation: false,
        read_state_store: GatewayReadStateStore.new(path: File.join(dir, "read-state.json")),
        attachment_store: PiAttachmentStore.new(root: File.join(dir, "attachments")),
        rpc_clients: active_rpc_clients,
        mark_selected_read: false,
        pending_sessions: [[session_path, missing_cwd]]
      )

      assert_empty view.all_sessions
      assert_nil view.selected_session
      assert File.exist?(session_path)
    end
  end

  def test_inactive_pending_background_session_is_not_listed
    Dir.mktmpdir do |dir|
      session_path = write_session(dir)
      pending_path = File.join(dir, "pending.jsonl")

      view = Sessions::SessionView.build(
        sessions_root: dir,
        params: { "session" => session_path },
        include_conversation: false,
        read_state_store: GatewayReadStateStore.new(path: File.join(dir, "read-state.json")),
        attachment_store: PiAttachmentStore.new(root: File.join(dir, "attachments")),
        rpc_clients: inactive_rpc_clients,
        mark_selected_read: false,
        pending_sessions: [[pending_path, dir]]
      )

      assert_equal [session_path], view.to_instance_variables.fetch(:@all_sessions).map(&:path)
    end
  end

  def test_conversation_applies_byte_budget_after_retaining_the_latest_turn
    Dir.mktmpdir do |dir|
      session_path = write_session_with_messages(dir, 100, text_suffix: "x" * 10_000)

      assignments = build_conversation(dir, session_path).to_instance_variables
      rendered_text = assignments.fetch(:@messages).map(&:text)

      assert_operator rendered_text.length, :<, 50
      assert_match(/\AMessage 100/, rendered_text.last)
      assert_equal 100 - rendered_text.length, assignments.fetch(:@conversation_older_message_count)
    end
  end

  def test_conversation_window_accounts_for_inline_image_bytes
    Dir.mktmpdir do |dir|
      path = File.join(dir, "session.jsonl")
      entries = [{ type: "session", id: "session", cwd: dir }] + (1..40).map do |index|
        {
          type: "message",
          message: {
            role: "user",
            content: [
              { type: "text", text: "Message #{index}" },
              { type: "image", mimeType: "image/png", data: "x" * 20_000 }
            ]
          }
        }
      end
      File.write(path, entries.map { |entry| JSON.generate(entry) }.join("\n") + "\n")

      assignments = build_conversation(dir, path).to_instance_variables

      assert_operator assignments.fetch(:@messages).length, :<, 40
      assert_equal "Message 40", assignments.fetch(:@messages).last.text
      assert assignments.fetch(:@conversation_has_older_messages)
    end
  end

  private

  def build_conversation(root, session_path)
    Sessions::SessionView.build(
      sessions_root: root,
      params: { "session" => session_path },
      include_conversation: true,
      read_state_store: GatewayReadStateStore.new(path: File.join(root, "read-state.json")),
      attachment_store: PiAttachmentStore.new(root: File.join(root, "attachments")),
      rpc_clients: inactive_rpc_clients,
      mark_selected_read: false
    )
  end

  def inactive_rpc_clients
    Class.new do
      def active?(_path)
        false
      end
    end.new
  end

  def write_session(root, id = "session", cwd: root, timestamp: nil)
    path = File.join(root, "#{id}.jsonl")
    File.write(path, [
      JSON.generate({ type: "session", id: id, cwd: cwd, timestamp: timestamp }),
      JSON.generate({ type: "message", timestamp: timestamp, message: { role: "user", content: [{ type: "text", text: "Hello" }] } })
    ].join("\n") + "\n")
    path
  end

  def write_session_with_messages(root, count, text_suffix: "")
    path = File.join(root, "session.jsonl")
    entries = [{ type: "session", id: "session", cwd: root }] + (1..count).map do |index|
      { type: "message", message: { role: "user", content: [{ type: "text", text: "Message #{index}#{text_suffix}" }] } }
    end
    File.write(path, entries.map { |entry| JSON.generate(entry) }.join("\n") + "\n")
    path
  end
end
