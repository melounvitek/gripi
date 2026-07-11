require "minitest/autorun"
require "stringio"
require "json"
require "open3"
require "timeout"
require_relative "../lib/pi_rpc_client"

class PiRpcClientTest < Minitest::Test
  def test_starts_pi_rpc_process_for_session_file
    calls = []
    input = StringIO.new
    output = StringIO.new
    popen = ->(*args) do
      calls << args
      [input, output, StringIO.new, Object.new]
    end

    client = PiRpcClient.start("/tmp/session.jsonl", popen: popen)

    assert_instance_of PiRpcClient, client
    assert_equal [["pi", "--mode", "rpc", "--extension", PiRpcClient::GATEWAY_EXTENSION_PATH, "--session", "/tmp/session.jsonl"]], calls.map { |args| args.drop(1) }
  end

  def test_removes_gateway_ruby_environment_when_starting_pi
    calls = []
    input = StringIO.new
    output = StringIO.new
    popen = ->(*args) do
      calls << args
      [input, output, StringIO.new, Object.new]
    end

    with_env(
      "BUNDLE_GEMFILE" => "/gateway/Gemfile",
      "BUNDLE_LOCKFILE" => "/gateway/Gemfile.lock",
      "BUNDLER_SETUP" => "/gateway/bundler/setup",
      "GEM_HOME" => "/gateway/gems",
      "RUBYOPT" => "-rbundler/setup",
      "PATH" => "/usr/bin"
    ) do
      PiRpcClient.start("/tmp/session.jsonl", popen: popen)
    end

    child_env = calls.fetch(0).fetch(0)
    assert_nil child_env.fetch("BUNDLE_GEMFILE")
    assert_nil child_env.fetch("BUNDLE_LOCKFILE")
    assert_nil child_env.fetch("BUNDLER_SETUP")
    assert_nil child_env.fetch("GEM_HOME")
    assert_nil child_env.fetch("RUBYOPT")
    assert_equal "/usr/bin", child_env.fetch("PATH")
  end

  def test_pi_process_env_unsets_ruby_environment_for_spawn
    with_env(
      "BUNDLE_GEMFILE" => "/gateway/Gemfile",
      "GEM_HOME" => "/gateway/gems",
      "RUBYOPT" => "-rbundler/setup"
    ) do
      stdout, stderr, status = Open3.capture3(PiRpcClient.pi_process_env, RbConfig.ruby, "-e", "puts [ENV['BUNDLE_GEMFILE'], ENV['GEM_HOME'], ENV['RUBYOPT']].compact")

      assert status.success?, stderr
      assert_empty stdout
    end
  end

  def test_starts_pi_rpc_process_with_configured_node_and_pi_paths
    calls = []
    input = StringIO.new
    output = StringIO.new
    popen = ->(*args) do
      calls << args
      [input, output, StringIO.new, Object.new]
    end

    client = PiRpcClient.start("/tmp/session.jsonl", command_prefix: ["/opt/node", "/opt/pi"], popen: popen)

    assert_instance_of PiRpcClient, client
    assert_equal [["/opt/node", "/opt/pi", "--mode", "rpc", "--extension", PiRpcClient::GATEWAY_EXTENSION_PATH, "--session", "/tmp/session.jsonl"]], calls.map { |args| args.drop(1) }
  end

  def test_starts_new_pi_rpc_process_in_cwd
    calls = []
    input = StringIO.new
    output = StringIO.new
    popen = ->(*args) do
      calls << args
      [input, output, StringIO.new, Object.new]
    end

    client = PiRpcClient.start_in_cwd("/tmp/project", popen: popen)

    assert_instance_of PiRpcClient, client
    assert_equal [["pi", "--mode", "rpc", "--extension", PiRpcClient::GATEWAY_EXTENSION_PATH, { chdir: "/tmp/project" }]], calls.map { |args| args.drop(1) }
  end

  def test_starts_new_pi_rpc_process_with_configured_node_and_pi_paths
    calls = []
    input = StringIO.new
    output = StringIO.new
    popen = ->(*args) do
      calls << args
      [input, output, StringIO.new, Object.new]
    end

    client = PiRpcClient.start_in_cwd("/tmp/project", command_prefix: ["/opt/node", "/opt/pi"], popen: popen)

    assert_instance_of PiRpcClient, client
    assert_equal [["/opt/node", "/opt/pi", "--mode", "rpc", "--extension", PiRpcClient::GATEWAY_EXTENSION_PATH, { chdir: "/tmp/project" }]], calls.map { |args| args.drop(1) }
  end

  def test_command_prefix_defaults_to_pi
    assert_equal ["pi"], PiRpcClient.command_prefix(node_path: nil, pi_path: nil)
  end

  def test_command_prefix_uses_configured_node_and_pi_paths
    assert_equal ["/opt/node", "/opt/pi"], PiRpcClient.command_prefix(node_path: " /opt/node ", pi_path: " /opt/pi ")
  end

  def test_command_prefix_requires_node_and_pi_paths_together
    error = assert_raises(ArgumentError) do
      PiRpcClient.command_prefix(node_path: "/opt/node", pi_path: nil)
    end

    assert_includes error.message, "PI_GATEWAY_NODE and PI_GATEWAY_PI must be set together"
  end

  def test_sends_jsonl_command_and_returns_matching_response
    input = StringIO.new
    output = StringIO.new(JSON.generate({ type: "event", name: "queued" }) + "\n" + JSON.generate({ id: "state-1", type: "state", cwd: "/tmp/project" }) + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    response = client.request("get_state", id: "state-1")

    assert_equal({ "id" => "state-1", "type" => "state", "cwd" => "/tmp/project" }, response)
    assert_equal({ events: [{ "type" => "event", "name" => "queued" }], last_seq: 1, missed: false }, client.events_after(0))
    assert_equal({ events: [{ "type" => "event", "name" => "queued" }], last_seq: 1, missed: false }, client.events_after(0))
    assert_equal({ events: [], last_seq: 1, missed: false }, client.events_after(1))
    written = JSON.parse(input.string.lines.first)
    assert_equal({ "id" => "state-1", "type" => "get_state" }, written)
  end

  def test_raises_clear_error_when_pi_process_exits_before_write
    stdin = Object.new
    def stdin.write(_payload)
      raise Errno::EPIPE
    end

    client = PiRpcClient.new(stdin: stdin, stdout: StringIO.new)

    error = assert_raises(IOError) do
      client.request("get_state", id: "state-1")
    end
    assert_includes error.message, "Pi RPC process exited before accepting command"
  end

  def test_snapshots_latest_updates_for_running_tools_with_the_event_cursor
    input = StringIO.new
    reader, writer = IO.pipe
    client = PiRpcClient.new(stdin: input, stdout: reader)

    writer.puts JSON.generate({ type: "tool_execution_start", toolCallId: "call-1", toolName: "subagent", args: {} })
    writer.puts JSON.generate({ type: "tool_execution_update", toolCallId: "call-1", toolName: "subagent", partialResult: { content: [{ type: "text", text: "first" }] } })
    latest_update = { type: "tool_execution_update", toolCallId: "call-1", toolName: "subagent", partialResult: { content: [{ type: "text", text: "latest" }] } }
    writer.puts JSON.generate(latest_update)
    writer.puts JSON.generate({ type: "tool_execution_update", toolCallId: "call-2", toolName: "custom_tool", partialResult: { content: [{ type: "text", text: "unrelated progress" }] } })
    writer.puts JSON.generate({ id: "state-1", type: "state" })
    client.request("get_state", id: "state-1")

    assert_equal({ event_sequence: 4, active_tool_events: [JSON.parse(JSON.generate(latest_update))] }, client.live_snapshot)

    writer.puts JSON.generate({ type: "tool_execution_end", toolCallId: "call-1", toolName: "subagent", result: { content: [{ type: "text", text: "done" }] } })
    writer.puts JSON.generate({ id: "state-2", type: "state" })
    client.request("get_state", id: "state-2")

    assert_equal({ event_sequence: 5, active_tool_events: [] }, client.live_snapshot)
  ensure
    writer&.close
    reader&.close
  end

  def test_clears_running_tool_snapshots_when_the_agent_ends
    input = StringIO.new
    reader, writer = IO.pipe
    client = PiRpcClient.new(stdin: input, stdout: reader)

    writer.puts JSON.generate({ type: "tool_execution_start", toolCallId: "call-1", toolName: "subagent", args: {} })
    writer.puts JSON.generate({ type: "agent_end" })
    writer.puts JSON.generate({ id: "state-1", type: "state" })
    client.request("get_state", id: "state-1")

    assert_equal({ event_sequence: 2, active_tool_events: [] }, client.live_snapshot)
  ensure
    writer&.close
    reader&.close
  end

  def test_bounds_large_running_tool_snapshots
    input = StringIO.new
    reader, writer = IO.pipe
    client = PiRpcClient.new(stdin: input, stdout: reader)
    request_thread = Thread.new { client.request("get_state", id: "state-1") }
    Timeout.timeout(1) { sleep 0.01 until input.string.include?("get_state") }

    writer.puts JSON.generate({
      type: "tool_execution_update",
      toolCallId: "call-1",
      toolName: "subagent",
      partialResult: { content: [{ type: "text", text: "x" * (PiRpcClient::MAX_ACTIVE_TOOL_SNAPSHOT_BYTES + 1) }] }
    })
    writer.puts JSON.generate({ id: "state-1", type: "state" })
    request_thread.join(1)

    event = client.live_snapshot.fetch(:active_tool_events).first
    assert_operator JSON.generate(event).bytesize, :<=, PiRpcClient::MAX_ACTIVE_TOOL_SNAPSHOT_BYTES
    assert_equal "Subagent is still running…", event.dig("partialResult", "content", 0, "text")
  ensure
    request_thread&.kill
    writer&.close
    reader&.close
  end

  def test_drops_running_tool_snapshots_with_oversized_ids
    input = StringIO.new
    reader, writer = IO.pipe
    client = PiRpcClient.new(stdin: input, stdout: reader)
    oversized_id = "x" * (PiRpcClient::MAX_ACTIVE_TOOL_SNAPSHOT_ID_BYTES + 1)

    writer.puts JSON.generate({ type: "tool_execution_start", toolCallId: oversized_id, toolName: "subagent", args: {} })
    writer.puts JSON.generate({ id: "state-1", type: "state" })
    client.request("get_state", id: "state-1")

    assert_empty client.live_snapshot.fetch(:active_tool_events)
  ensure
    writer&.close
    reader&.close
  end

  def test_limits_concurrent_running_tool_snapshots
    input = StringIO.new
    reader, writer = IO.pipe
    client = PiRpcClient.new(stdin: input, stdout: reader)

    (PiRpcClient::MAX_ACTIVE_TOOL_SNAPSHOTS + 1).times do |index|
      writer.puts JSON.generate({ type: "tool_execution_start", toolCallId: "call-#{index}", toolName: "subagent", args: {} })
    end
    writer.puts JSON.generate({ id: "state-1", type: "state" })
    client.request("get_state", id: "state-1")

    assert_equal PiRpcClient::MAX_ACTIVE_TOOL_SNAPSHOTS, client.live_snapshot.fetch(:active_tool_events).length
  ensure
    writer&.close
    reader&.close
  end

  def test_reports_missed_events_when_cursor_precedes_buffer
    input = StringIO.new
    output = StringIO.new([
      JSON.generate({ type: "event", name: "one" }),
      JSON.generate({ type: "event", name: "two" }),
      JSON.generate({ id: "state-1", type: "state" })
    ].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output, event_buffer_limit: 1)

    client.request("get_state", id: "state-1")

    assert_equal({ events: [], last_seq: 2, missed: true }, client.events_after(0))
    assert_equal({ events: [{ "type" => "event", "name" => "two" }], last_seq: 2, missed: false }, client.events_after(1))
  end

  def test_tracks_busy_state_from_agent_events
    now = Time.at(1_000)
    input = StringIO.new
    reader, writer = IO.pipe
    client = PiRpcClient.new(stdin: input, stdout: reader, clock: -> { now })

    refute client.busy?
    assert_nil client.busy_since
    writer.puts JSON.generate({ type: "agent_start" })
    writer.puts JSON.generate({ id: "state-1", type: "state" })
    client.request("get_state", id: "state-1")
    assert client.busy?
    assert_equal Time.at(1_000), client.busy_since
    assert client.agent_running?

    now = Time.at(1_005)
    writer.puts JSON.generate({ type: "turn_end" })
    writer.puts JSON.generate({ id: "state-2", type: "state" })
    client.request("get_state", id: "state-2")
    assert client.busy?
    assert_equal Time.at(1_000), client.busy_since
    assert client.agent_running?

    now = Time.at(1_010)
    writer.puts JSON.generate({ type: "agent_end" })
    writer.puts JSON.generate({ id: "state-3", type: "state" })
    client.request("get_state", id: "state-3")
    refute client.busy?
    assert_nil client.busy_since
    refute client.agent_running?
  ensure
    writer&.close
    reader&.close
  end

  def test_tracks_busy_state_from_turn_events_when_agent_events_are_absent
    now = Time.at(1_000)
    input = StringIO.new
    reader, writer = IO.pipe
    client = PiRpcClient.new(stdin: input, stdout: reader, clock: -> { now })

    writer.puts JSON.generate({ type: "turn_start" })
    writer.puts JSON.generate({ id: "state-1", type: "state" })
    client.request("get_state", id: "state-1")

    assert client.busy?
    assert_equal Time.at(1_000), client.busy_since

    now = Time.at(1_005)
    writer.puts JSON.generate({ type: "turn_end" })
    writer.puts JSON.generate({ id: "state-2", type: "state" })
    client.request("get_state", id: "state-2")

    refute client.busy?
    assert_nil client.busy_since
  ensure
    writer&.close
    reader&.close
  end

  def test_tracks_compacting_state_from_compaction_events
    now = Time.at(1_000)
    input = StringIO.new
    reader, writer = IO.pipe
    client = PiRpcClient.new(stdin: input, stdout: reader, clock: -> { now })

    writer.puts JSON.generate({ type: "compaction_start" })
    writer.puts JSON.generate({ id: "state-1", type: "state" })
    client.request("get_state", id: "state-1")

    assert client.busy?
    assert client.compacting?
    assert_equal Time.at(1_000), client.busy_since

    writer.puts JSON.generate({ type: "compaction" })
    writer.puts JSON.generate({ id: "state-2", type: "state" })
    client.request("get_state", id: "state-2")

    refute client.busy?
    refute client.compacting?
    assert_nil client.busy_since
  ensure
    writer&.close
    reader&.close
  end

  def test_keeps_compacting_state_after_compact_response_until_compaction_end
    input = StringIO.new
    reader, writer = IO.pipe
    client = PiRpcClient.new(stdin: input, stdout: reader)

    thread = Thread.new { client.compact }
    Timeout.timeout(1) { sleep 0.01 until input.string.include?("compact") }
    writer.puts JSON.generate({ type: "compaction_start" })
    writer.puts JSON.generate({ id: "compact-1", type: "response", command: "compact", success: true })
    thread.join(1)

    assert client.compacting?
    assert client.busy?

    writer.puts JSON.generate({ type: "compaction_end" })
    writer.puts JSON.generate({ id: "state-1", type: "state" })
    client.request("get_state", id: "state-1")

    refute client.compacting?
    refute client.busy?
  ensure
    thread&.kill
    writer&.close
    reader&.close
  end

  def test_clears_busy_state_when_reader_exits
    input = StringIO.new
    output = StringIO.new([
      JSON.generate({ type: "turn_start" }),
      JSON.generate({ type: "tool_execution_start", toolCallId: "call-1", toolName: "subagent", args: {} })
    ].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    client.events_after(0)
    sleep 0.05

    refute client.busy?
    assert_nil client.busy_since
    assert_empty client.live_snapshot.fetch(:active_tool_events)
  end

  def test_includes_payload_fields_in_command
    input = StringIO.new
    output = StringIO.new(JSON.generate({ id: "prompt-1", type: "accepted" }) + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    client.request("prompt", id: "prompt-1", message: "Hello", images: [{ type: "image", data: "abc", mimeType: "image/png" }])

    assert_equal({ "id" => "prompt-1", "type" => "prompt", "message" => "Hello", "images" => [{ "type" => "image", "data" => "abc", "mimeType" => "image/png" }] }, JSON.parse(input.string))
  end

  def test_tree_leaf_reports_current_leaf_from_extension_bridge
    input = StringIO.new
    output = StringIO.new([
      JSON.generate({ type: "extension_ui_request", method: "setStatus", statusKey: "pi_web_tree_leaf:abc123", statusText: "entry-9" }),
      JSON.generate({ id: "prompt-1", type: "response", command: "prompt", success: true })
    ].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    leaf_id = with_secure_random_hex("abc123") do
      client.tree_leaf
    end

    assert_equal "entry-9", leaf_id
    assert_equal({ "id" => "prompt-1", "type" => "prompt", "message" => "/pi_web_tree_leaf abc123" }, JSON.parse(input.string))
  end

  def test_navigate_tree_reports_cancelled_status_from_extension_bridge
    input = StringIO.new
    output = StringIO.new([
      JSON.generate({ type: "extension_ui_request", method: "setStatus", statusKey: "pi_web_tree:abc123", statusText: "cancelled" }),
      JSON.generate({ id: "prompt-1", type: "response", command: "prompt", success: true })
    ].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    response = with_secure_random_hex("abc123") do
      client.navigate_tree("entry-2")
    end

    assert_equal true, response.fetch("data").fetch("cancelled")
    assert_equal({ "id" => "prompt-1", "type" => "prompt", "message" => "/pi_web_tree entry-2 abc123" }, JSON.parse(input.string))
  end

  def test_command_helpers_send_supported_rpc_commands
    input = StringIO.new
    output = StringIO.new([
      JSON.generate({ id: "get_state-1", type: "state" }),
      JSON.generate({ id: "get_messages-2", type: "messages" }),
      JSON.generate({ id: "prompt-3", type: "accepted" }),
      JSON.generate({ id: "steer-4", type: "response", command: "steer", success: true }),
      JSON.generate({ id: "abort-5", type: "aborted" }),
      JSON.generate({ id: "new_session-6", type: "response", command: "new_session", success: true, data: { cancelled: false } }),
      JSON.generate({ id: "switch_session-7", type: "response", command: "switch_session", success: true, data: { cancelled: false } }),
      JSON.generate({ id: "get_commands-8", type: "response", command: "get_commands", success: true, data: { commands: [] } }),
      JSON.generate({ id: "compact-9", type: "response", command: "compact", success: true, data: {} }),
      JSON.generate({ id: "set_session_name-10", type: "response", command: "set_session_name", success: true }),
      JSON.generate({ id: "get_fork_messages-11", type: "response", command: "get_fork_messages", success: true, data: { messages: [] } }),
      JSON.generate({ id: "fork-12", type: "response", command: "fork", success: true, data: { text: "Hello", cancelled: false } }),
      JSON.generate({ id: "clone-13", type: "response", command: "clone", success: true, data: { cancelled: false } }),
      JSON.generate({ id: "prompt-14", type: "response", command: "prompt", success: true }),
      JSON.generate({ id: "prompt-15", type: "response", command: "prompt", success: true }),
      JSON.generate({ id: "follow_up-16", type: "response", command: "follow_up", success: true }),
      JSON.generate({ id: "get_available_models-17", type: "response", command: "get_available_models", success: true, data: { models: [] } }),
      JSON.generate({ id: "set_model-18", type: "response", command: "set_model", success: true, data: {} }),
      JSON.generate({ id: "set_thinking_level-19", type: "response", command: "set_thinking_level", success: true }),
      JSON.generate({ id: "cycle_thinking_level-20", type: "response", command: "cycle_thinking_level", success: true, data: { level: "high" } })
    ].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    client.get_state
    client.get_messages
    client.prompt("Hello", [{ type: "image", data: "abc", mimeType: "image/png" }])
    client.steer("Redirect now")
    client.abort
    client.new_session("/tmp/session.jsonl")
    client.switch_session("/tmp/other-session.jsonl")
    client.get_commands
    client.compact("Focus summary")
    client.set_session_name("Useful name")
    client.get_fork_messages
    client.fork("entry-1")
    client.clone_session
    with_secure_random_hex("abc123") do
      client.navigate_tree("entry-2")
    end
    with_secure_random_hex("def456") do
      client.tree_leaf
    end
    client.follow_up("After done", [{ type: "image", data: "def", mimeType: "image/jpeg" }])
    client.get_available_models
    client.set_model("anthropic", "claude-sonnet-4")
    client.set_thinking_level("high")
    client.cycle_thinking_level

    assert_equal [
      { "id" => "get_state-1", "type" => "get_state" },
      { "id" => "get_messages-2", "type" => "get_messages" },
      { "id" => "prompt-3", "type" => "prompt", "message" => "Hello", "images" => [{ "type" => "image", "data" => "abc", "mimeType" => "image/png" }] },
      { "id" => "steer-4", "type" => "steer", "message" => "Redirect now" },
      { "id" => "abort-5", "type" => "abort" },
      { "id" => "new_session-6", "type" => "new_session", "parentSession" => "/tmp/session.jsonl" },
      { "id" => "switch_session-7", "type" => "switch_session", "sessionPath" => "/tmp/other-session.jsonl" },
      { "id" => "get_commands-8", "type" => "get_commands" },
      { "id" => "compact-9", "type" => "compact", "customInstructions" => "Focus summary" },
      { "id" => "set_session_name-10", "type" => "set_session_name", "name" => "Useful name" },
      { "id" => "get_fork_messages-11", "type" => "get_fork_messages" },
      { "id" => "fork-12", "type" => "fork", "entryId" => "entry-1" },
      { "id" => "clone-13", "type" => "clone" },
      { "id" => "prompt-14", "type" => "prompt", "message" => "/pi_web_tree entry-2 abc123" },
      { "id" => "prompt-15", "type" => "prompt", "message" => "/pi_web_tree_leaf def456" },
      { "id" => "follow_up-16", "type" => "follow_up", "message" => "After done", "images" => [{ "type" => "image", "data" => "def", "mimeType" => "image/jpeg" }] },
      { "id" => "get_available_models-17", "type" => "get_available_models" },
      { "id" => "set_model-18", "type" => "set_model", "provider" => "anthropic", "modelId" => "claude-sonnet-4" },
      { "id" => "set_thinking_level-19", "type" => "set_thinking_level", "level" => "high" },
      { "id" => "cycle_thinking_level-20", "type" => "cycle_thinking_level" }
    ], input.string.lines.map { |line| JSON.parse(line) }
  end

  private

  def with_env(values)
    old_values = values.keys.to_h { |key| [key, ENV[key]] }
    values.each do |key, value|
      value.nil? ? ENV.delete(key) : ENV[key] = value
    end
    yield
  ensure
    old_values.each do |key, value|
      value.nil? ? ENV.delete(key) : ENV[key] = value
    end
  end

  def with_secure_random_hex(value)
    original = SecureRandom.method(:hex)
    SecureRandom.define_singleton_method(:hex) { |_length = nil| value }
    yield
  ensure
    SecureRandom.define_singleton_method(:hex, original)
  end
end
