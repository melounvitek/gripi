require "minitest/autorun"
require "rack/mock"
require "tmpdir"
require "json"
require "fileutils"
require_relative "../app"

class AppTest < Minitest::Test
  def setup
    PiWebGateway.set :active_rpc_client, nil
    PiWebGateway.set :active_rpc_session, nil
    PiWebGateway.set :active_rpc_cwd, nil
  end

  def test_posts_prompt_to_selected_session_and_redirects_back
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :active_rpc_client, nil
      PiWebGateway.set :active_rpc_session, nil
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/prompt",
        params: { "session" => path, "message" => "Hello Pi" }
      )

      assert_equal 303, response.status
      assert_equal [[ :start, path ], [ :prompt, "Hello Pi" ]], calls
      assert_includes response["Location"], Rack::Utils.escape(path)
    end
  end

  def test_returns_buffered_rpc_events_for_selected_session
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :active_rpc_client, nil
      PiWebGateway.set :active_rpc_session, nil
      client = FakeRpcClient.new(calls, [{ "type" => "assistant_delta", "text" => "Hi" }])
      PiWebGateway.set :active_rpc_client, client
      PiWebGateway.set :active_rpc_session, path
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/events",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_equal "application/json", response.content_type
      assert_equal({ "events" => [{ "type" => "assistant_delta", "text" => "Hi" }] }, JSON.parse(response.body))
      assert_equal [[ :drain_events ]], calls
    end
  end

  def test_ignores_event_polls_for_inactive_sessions
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :active_rpc_session, "other-session"
      PiWebGateway.set :active_rpc_client, FakeRpcClient.new(calls, [{ "type" => "stale" }])
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/events",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_equal({ "events" => [] }, JSON.parse(response.body))
      assert_empty calls
    end
  end

  def test_creates_new_native_session_and_redirects_to_it
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      new_path = File.join(File.dirname(path), "new-session.jsonl")
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :active_rpc_client, nil
      PiWebGateway.set :active_rpc_session, nil
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls, [], new_path)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/sessions/new",
        params: { "session" => path }
      )

      assert_equal 303, response.status
      assert_includes response["Location"], Rack::Utils.escape(new_path)
      assert_equal [[ :start, path ], [ :new_session, path ], [ :get_state ]], calls
    end
  end

  def test_hides_commands_for_selected_session
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :active_rpc_client, nil
      PiWebGateway.set :active_rpc_session, nil
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls, [{ "name" => "review", "source" => "skill", "description" => "Review code" }])
      }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      refute_includes response.body, "/review"
      refute_includes response.body, "Review code"
      assert_equal [[ :start, path ], [ :get_commands ]], calls
    end
  end

  def test_aborts_selected_session
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :active_rpc_client, nil
      PiWebGateway.set :active_rpc_session, nil
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/abort",
        params: { "session" => path }
      )

      assert_equal 303, response.status
      assert_equal [[ :start, path ], [ :abort ]], calls
    end
  end

  def test_compacts_selected_session
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :active_rpc_client, nil
      PiWebGateway.set :active_rpc_session, nil
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/compact",
        params: { "session" => path, "instructions" => "recent work" }
      )

      assert_equal 303, response.status
      assert_equal [[ :start, path ], [ :compact, "recent work" ]], calls
    end
  end

  def test_renames_selected_session
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :active_rpc_client, nil
      PiWebGateway.set :active_rpc_session, nil
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/rename",
        params: { "session" => path, "name" => "Useful name" }
      )

      assert_equal 303, response.status
      assert_equal [[ :start, path ], [ :set_session_name, "Useful name" ]], calls
    end
  end

  def test_renders_pending_new_session_before_pi_persists_the_file
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      pending_path = File.join(File.dirname(path), "pending-session.jsonl")
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :active_rpc_session, pending_path
      PiWebGateway.set :active_rpc_cwd, "/tmp/project"

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => pending_path }
      )

      assert_equal 200, response.status
      assert_includes response.body, "New session (pending first assistant response)"
      assert_includes response.body, pending_path
    end
  end

  def test_renders_discord_like_scrolling_shell
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :active_rpc_client, nil
      PiWebGateway.set :active_rpc_session, nil
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes response.body, "app-shell"
      assert_includes response.body, "session-sidebar"
      assert_includes response.body, "conversation-panel"
      assert_includes response.body, "session-header"
      assert_includes response.body, "conversation-scroll"
      assert_includes response.body, "composer"
      assert_includes response.body, "nearConversationBottom"
    end
  end

  def test_trims_sidebar_sessions_to_latest_five_by_default
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 7)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => paths.last }
      )

      assert_equal 200, response.status
      assert_includes response.body, "Show all 7"
      assert_includes response.body, "Session 7"
      assert_includes response.body, "Session 3"
      refute_includes response.body, "Session 2"
      refute_includes response.body, "Session 1"
    end
  end

  def test_keeps_older_selected_session_visible_when_sidebar_is_trimmed
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 7)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => paths.first }
      )

      assert_equal 200, response.status
      assert_includes response.body, "Session 1"
      assert_includes response.body, "selected"
    end
  end

  def test_expands_sidebar_cwd_group_to_show_all_sessions
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 7)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => paths.last, "expanded_cwd" => ["/tmp/project"] }
      )

      assert_equal 200, response.status
      assert_includes response.body, "Show fewer"
      assert_includes response.body, "Session 7"
      assert_includes response.body, "Session 1"
    end
  end

  def test_renders_messages_with_role_specific_structure
    Dir.mktmpdir do |dir|
      path = write_session_with_messages(dir, [
        { role: "user", text: "Hello <Pi>" },
        { role: "assistant", text: "Hi there" },
        { role: "system", text: "System note" },
        { role: "custom", text: "Session renamed" },
        { role: "toolResult", text: "Tool output" },
        { role: "error", text: "Something failed" }
      ])
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes response.body, 'class="message message--user" data-role="user"'
      assert_includes response.body, 'class="message message--assistant" data-role="assistant"'
      assert_includes response.body, 'class="message message--status" data-role="system"'
      assert_includes response.body, 'class="message message--status" data-role="custom"'
      assert_includes response.body, 'message--tool'
      assert_includes response.body, 'data-role="toolResult"'
      assert_includes response.body, 'class="message message--error" data-role="error"'
      assert_includes response.body, 'class="message-body"'
      assert_includes response.body, "Hello &lt;Pi&gt;"
      refute_includes response.body, "Hello <Pi>"
      assert_includes response.body, "messageRoleKey"
    end
  end

  def test_renders_tool_and_thinking_messages_as_compact_details
    Dir.mktmpdir do |dir|
      path = write_session_with_raw_messages(dir, [
        {
          type: "message",
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "assistant",
            content: [
              { type: "thinking", thinking: "Thinking through the problem" },
              { type: "toolCall", name: "bash", arguments: { command: "ls" } }
            ],
            stopReason: "toolUse"
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:01:00Z",
          message: {
            role: "toolResult",
            toolName: "bash",
            content: [{ type: "text", text: "file list" }],
            isError: false
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:02:00Z",
          message: {
            role: "toolResult",
            toolName: "edit",
            content: [{ type: "text", text: "No match" }],
            isError: true
          }
        }
      ])
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes response.body, 'class="message message--assistant message--compact" data-role="assistant"'
      assert_includes response.body, '<summary><span class="compact-summary">thinking + bash</span></summary>'
      assert_includes response.body, 'class="message message--tool message--compact" data-role="toolResult"'
      assert_includes response.body, '<summary><span class="compact-summary">bash</span></summary>'
      assert_includes response.body, 'class="message message--tool message--compact message--tool-error" data-role="toolResult"'
      assert_includes response.body, '<details class="message-details" open>'
      assert_includes response.body, "Thinking through the problem"
      assert_includes response.body, "file list"
    end
  end

  def test_live_event_script_supports_compact_tool_rendering
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes response.body, "function contentInfo(content, message = {})"
      assert_includes response.body, "appendCompactMessage(roleName, info.summary, info.text, info.expanded"
      assert_includes response.body, "part.type === \"toolCall\""
      assert_includes response.body, "part.type === \"thinking\""
    end
  end

  def test_live_event_script_keeps_assistant_and_status_roles_separate
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes response.body, "let liveAssistantMessage = null;"
      assert_includes response.body, "let liveAssistantSeen = false;"
      assert_includes response.body, 'if (roleName === "user") {'
      assert_includes response.body, 'appendMessage("assistant", text, true, shouldScroll);'
      assert_includes response.body, 'if (["custom", "system", "status"].includes(role)) return "status";'
      assert_includes response.body, "showStatus(eventStatusText(event));"
      assert_includes response.body, 'if (liveAssistantSeen) showStatus("Done");'
      assert_includes response.body, "liveAssistantMessage = null;\n      liveAssistantSeen = false;\n      appendMessage(\"user\", message, true, true);"
    end
  end

  private

  class FakeRpcClient
    def initialize(calls, events_or_commands = [], session_file = nil)
      @calls = calls
      @events = events_or_commands
      @commands = events_or_commands
      @session_file = session_file
    end

    def prompt(message)
      @calls << [:prompt, message]
    end

    def get_messages
      @calls << [:get_messages]
    end

    def new_session(parent_session = nil)
      @calls << [:new_session, parent_session]
      { "type" => "response", "command" => "new_session", "success" => true, "data" => { "cancelled" => false } }
    end

    def get_state
      @calls << [:get_state]
      { "type" => "response", "command" => "get_state", "success" => true, "data" => { "sessionFile" => @session_file } }
    end

    def get_commands
      @calls << [:get_commands]
      { "type" => "response", "command" => "get_commands", "success" => true, "data" => { "commands" => @commands } }
    end

    def abort
      @calls << [:abort]
    end

    def compact(instructions = nil)
      @calls << [:compact, instructions]
    end

    def set_session_name(name)
      @calls << [:set_session_name, name]
    end

    def drain_events
      @calls << [:drain_events]
      @events
    end

    def close
      @calls << [:close]
    end
  end

  def write_session(root)
    session_dir = File.join(root, "--project--")
    FileUtils.mkdir_p(session_dir)
    path = File.join(session_dir, "session.jsonl")
    File.write(path, JSON.generate({ type: "session", id: "session-1", cwd: "/tmp/project" }) + "\n")
    path
  end

  def write_sessions(root, count:)
    session_dir = File.join(root, "--project--")
    FileUtils.mkdir_p(session_dir)

    (1..count).map do |index|
      path = File.join(session_dir, "session-#{index}.jsonl")
      File.write(path, [
        JSON.generate({ type: "session", id: "session-#{index}", cwd: "/tmp/project" }),
        JSON.generate({ type: "session_info", name: "Session #{index}" })
      ].join("\n") + "\n")
      FileUtils.touch(path, mtime: Time.at(index))
      path
    end
  end

  def write_session_with_messages(root, messages)
    entries = messages.map.with_index do |message, index|
      {
        type: "message",
        timestamp: "2026-06-13T10:0#{index}:00Z",
        message: { role: message.fetch(:role), content: [{ type: "text", text: message.fetch(:text) }] }
      }
    end
    write_session_with_raw_messages(root, entries)
  end

  def write_session_with_raw_messages(root, messages)
    session_dir = File.join(root, "--project--")
    FileUtils.mkdir_p(session_dir)
    path = File.join(session_dir, "messages.jsonl")
    entries = [{ type: "session", id: "session-1", cwd: "/tmp/project" }] + messages
    File.write(path, entries.map { |entry| JSON.generate(entry) }.join("\n") + "\n")
    path
  end
end
