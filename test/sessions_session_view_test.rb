ENV["PI_GATEWAY_ADMIN_PASSWORD"] ||= "test-password"

require "minitest/autorun"
require "tmpdir"
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
        mark_selected_read: true,
        pending_session_cwd: ->(_path) {}
      )

      assignments = view.to_instance_variables
      assert_instance_of PiSessionStore, assignments.fetch(:@store)
      assert_equal [session_path], assignments.fetch(:@groups).values.flatten.map(&:path)
      assert_equal session_path, assignments.fetch(:@selected_session).path
      assert_instance_of Sessions::Sidebar, assignments.fetch(:@sidebar)
      assert_equal ["Hello"], assignments.fetch(:@messages).map(&:text)
      assert_equal({}, assignments.fetch(:@attachment_counts))
      assert_instance_of PiSessionStore::Status, assignments.fetch(:@session_status)
      refute assignments.fetch(:@current_tree_leaf_known)
      refute assignments.fetch(:@viewing_older_tree_leaf)
    end
  end

  private

  def inactive_rpc_clients
    Class.new do
      def active?(_path)
        false
      end
    end.new
  end

  def write_session(root)
    path = File.join(root, "session.jsonl")
    File.write(path, [
      JSON.generate({ type: "session", id: "session", cwd: root }),
      JSON.generate({ type: "message", message: { role: "user", content: [{ type: "text", text: "Hello" }] } })
    ].join("\n") + "\n")
    path
  end
end
