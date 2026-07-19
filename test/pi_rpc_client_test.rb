require "minitest/autorun"
require "stringio"
require "json"
require "base64"
require "open3"
require "timeout"
require "tmpdir"
require "rbconfig"
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
    assert_equal [["pi", "--mode", "rpc", "--extension", PiRpcClient::GRIPI_EXTENSION_PATH, "--session", "/tmp/session.jsonl", PiRpcClient.process_group_options]], calls.map { |args| args.drop(1) }
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
    assert_equal [["/opt/node", "/opt/pi", "--mode", "rpc", "--extension", PiRpcClient::GRIPI_EXTENSION_PATH, "--session", "/tmp/session.jsonl", PiRpcClient.process_group_options]], calls.map { |args| args.drop(1) }
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
    assert_equal [["pi", "--mode", "rpc", "--extension", PiRpcClient::GRIPI_EXTENSION_PATH, PiRpcClient.process_group_options.merge(chdir: "/tmp/project")]], calls.map { |args| args.drop(1) }
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
    assert_equal [["/opt/node", "/opt/pi", "--mode", "rpc", "--extension", PiRpcClient::GRIPI_EXTENSION_PATH, PiRpcClient.process_group_options.merge(chdir: "/tmp/project")]], calls.map { |args| args.drop(1) }
  end

  def test_close_terminates_the_pi_process_group
    skip "POSIX process groups are not available on Windows" if Gem.win_platform?

    Dir.mktmpdir do |dir|
      pid_path = File.join(dir, "descendant.pid")
      script_path = File.join(dir, "process_tree.rb")
      File.write(script_path, <<~RUBY)
        require "rbconfig"
        descendant = spawn(RbConfig.ruby, "-e", 'trap("TERM") {}; loop { sleep 1 }')
        File.write(ARGV.fetch(0), descendant.to_s)
        sleep 60
      RUBY
      stdin, stdout, stderr, wait_thread = Open3.popen3(RbConfig.ruby, script_path, pid_path, pgroup: true)
      Timeout.timeout(1) { sleep 0.01 until File.exist?(pid_path) }
      descendant_pid = File.read(pid_path).to_i
      sleep 0.1
      client = PiRpcClient.new(
        stdin: stdin,
        stdout: stdout,
        stderr: stderr,
        wait_thread: wait_thread,
        process_group: true,
        process_term_timeout: 0.05,
        process_kill_timeout: 0.5
      )

      client.close

      refute process_running?(wait_thread.pid)
      Timeout.timeout(1) { sleep 0.01 while process_running?(descendant_pid) }
      refute process_running?(descendant_pid)
    ensure
      Process.kill("KILL", descendant_pid) if descendant_pid && process_running?(descendant_pid)
      Process.kill("KILL", -wait_thread.pid) if wait_thread&.pid && process_running?(wait_thread.pid)
    end
  end

  def test_process_watcher_terminates_descendants_when_pi_exits_first
    skip "POSIX process groups are not available on Windows" if Gem.win_platform?

    Dir.mktmpdir do |dir|
      pid_path = File.join(dir, "descendant.pid")
      script_path = File.join(dir, "exiting_parent.rb")
      File.write(script_path, <<~RUBY)
        require "rbconfig"
        descendant = spawn(RbConfig.ruby, "-e", 'loop { sleep 1 }')
        File.write(ARGV.fetch(0), descendant.to_s)
      RUBY
      stdin, stdout, stderr, wait_thread = Open3.popen3(RbConfig.ruby, script_path, pid_path, pgroup: true)
      client = PiRpcClient.new(
        stdin: stdin,
        stdout: stdout,
        stderr: stderr,
        wait_thread: wait_thread,
        process_group: true,
        process_term_timeout: 0.05,
        process_kill_timeout: 0.5
      )
      Timeout.timeout(1) { sleep 0.01 until File.exist?(pid_path) }
      descendant_pid = File.read(pid_path).to_i

      Timeout.timeout(1) { sleep 0.01 while process_running?(descendant_pid) }
      refute process_running?(descendant_pid)
      client.close
    ensure
      Process.kill("KILL", descendant_pid) if descendant_pid && process_running?(descendant_pid)
      Process.kill("KILL", -wait_thread.pid) if wait_thread&.pid && process_running?(wait_thread.pid)
    end
  end

  def test_concurrent_close_waits_for_process_cleanup
    close_started = Queue.new
    release_close = Queue.new
    input = Object.new
    input.define_singleton_method(:closed?) { false }
    input.define_singleton_method(:close) do
      close_started << true
      release_close.pop
    end
    client = PiRpcClient.new(stdin: input, stdout: StringIO.new)
    first = Thread.new { client.close }
    close_started.pop
    second_finished = Queue.new
    second = Thread.new do
      client.close
      second_finished << true
    end

    refute second_finished.pop(timeout: 0.05)
    release_close << true
    assert second_finished.pop(timeout: 1)
  ensure
    release_close << true if first&.alive?
    first&.join
    second&.join
  end

  def test_close_does_not_signal_a_pid_after_the_original_waiter_has_exited
    unrelated_pid = spawn(RbConfig.ruby, "-e", "sleep 60")
    waiter = Struct.new(:pid) do
      def alive? = false
    end.new(unrelated_pid)
    client = PiRpcClient.new(stdin: StringIO.new, stdout: StringIO.new, wait_thread: waiter)

    client.close
    client.close

    assert process_running?(unrelated_pid)
  ensure
    Process.kill("KILL", unrelated_pid) if unrelated_pid && process_running?(unrelated_pid)
    Process.wait(unrelated_pid) if unrelated_pid
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

    assert_includes error.message, "GRIPI_NODE and GRIPI_PI must be set together"
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

  def test_get_session_stats_sends_rpc_command
    input = StringIO.new
    output = StringIO.new(JSON.generate({ id: "get_session_stats-1", type: "response", command: "get_session_stats", success: true, data: { contextUsage: { tokens: 50_000 } } }) + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    response = client.get_session_stats

    assert_equal 50_000, response.dig("data", "contextUsage", "tokens")
    assert_equal({ "id" => "get_session_stats-1", "type" => "get_session_stats" }, JSON.parse(input.string.lines.first))
  end

  def test_session_position_reports_known_persisted_entry_and_selected_leaf
    input = StringIO.new
    output = StringIO.new(JSON.generate({ id: "get_entries-1", type: "response", command: "get_entries", success: true, data: { entries: [], leafId: "selected-leaf" } }) + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    result = client.session_position("persisted-leaf")

    assert_equal({ known: true, leaf_id: "selected-leaf", error: nil }, result)
    assert_equal({ "id" => "get_entries-1", "type" => "get_entries", "since" => "persisted-leaf" }, JSON.parse(input.string.lines.first))
  end

  def test_session_entries_after_returns_rpc_suffix_and_selected_leaf
    input = StringIO.new
    entries = [{ "type" => "message", "id" => "entry-2" }, { "type" => "message", "id" => "entry-3" }]
    output = StringIO.new(JSON.generate({ id: "get_entries-1", type: "response", command: "get_entries", success: true, data: { entries: entries, leafId: "entry-3" } }) + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    result = client.session_entries_after("entry-1")

    assert_equal true, result[:known]
    assert_equal "entry-3", result[:leaf_id]
    assert_equal entries, result[:entries]
    assert_nil result[:error]
  end

  def test_session_position_reports_entry_unknown_to_rpc_process
    input = StringIO.new
    output = StringIO.new(JSON.generate({ id: "get_entries-1", type: "response", command: "get_entries", success: false, error: "Entry not found: external-leaf" }) + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    result = client.session_position("external-leaf")

    assert_equal({ known: false, leaf_id: nil, error: nil }, result)
  end

  def test_session_position_fails_closed_for_unsupported_rpc_command
    input = StringIO.new
    output = StringIO.new(JSON.generate({ id: "get_entries-1", type: "response", command: "get_entries", success: false, error: "Unknown command type: get_entries" }) + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    result = client.session_position("persisted-leaf")

    assert_equal false, result[:known]
    assert_nil result[:leaf_id]
    assert_includes result[:error], "Unknown command"
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

  def test_correlates_concurrent_requests_when_responses_arrive_out_of_order
    command_reader, command_writer = IO.pipe
    response_reader, response_writer = IO.pipe
    client = PiRpcClient.new(stdin: command_writer, stdout: response_reader)

    state_thread = Thread.new { client.get_state }
    state_command = JSON.parse(Timeout.timeout(1) { command_reader.gets })
    abort_thread = Thread.new { client.abort }
    abort_command = JSON.parse(Timeout.timeout(1) { command_reader.gets })

    response_writer.puts JSON.generate({ id: abort_command.fetch("id"), type: "response", command: "abort", success: true })
    response_writer.puts JSON.generate({ id: state_command.fetch("id"), type: "response", command: "get_state", success: true })

    assert_equal "abort", Timeout.timeout(1) { abort_thread.value }.fetch("command")
    assert_equal "get_state", Timeout.timeout(1) { state_thread.value }.fetch("command")
    assert_equal ["get_state", "abort"], [state_command.fetch("type"), abort_command.fetch("type")]
    refute_equal state_command.fetch("id"), abort_command.fetch("id")
  ensure
    state_thread&.kill
    abort_thread&.kill
    command_reader&.close
    command_writer&.close
    response_reader&.close
    response_writer&.close
  end

  def test_raises_clear_error_when_pi_process_exits_before_responding
    command_reader, command_writer = IO.pipe
    response_reader, response_writer = IO.pipe
    client = PiRpcClient.new(stdin: command_writer, stdout: response_reader)

    request_thread = Thread.new { client.get_state }
    request_thread.report_on_exception = false
    Timeout.timeout(1) { command_reader.gets }
    response_writer.close

    error = assert_raises(IOError) { Timeout.timeout(1) { request_thread.value } }
    assert_includes error.message, "Pi RPC process exited before responding"
  ensure
    request_thread&.kill
    command_reader&.close
    command_writer&.close
    response_reader&.close
    response_writer&.close unless response_writer&.closed?
  end

  def test_times_out_when_pi_stdin_stops_accepting_commands
    command_reader, command_writer = IO.pipe
    begin
      loop { command_writer.write_nonblock("x" * 4096) }
    rescue IO::WaitWritable
      nil
    end
    response_reader, response_writer = IO.pipe
    client = PiRpcClient.new(stdin: command_writer, stdout: response_reader, request_timeout: 0.05)
    request_thread = Thread.new { client.get_state }
    request_thread.report_on_exception = false

    error = assert_raises(PiRpcClient::RequestTimeout) { Timeout.timeout(1) { request_thread.value } }
    assert_includes error.message, "get_state"
  ensure
    request_thread&.kill
    command_reader&.close
    command_writer&.close
    response_reader&.close
    response_writer&.close
  end

  def test_drains_pi_stderr_while_waiting_for_responses
    command_reader, command_writer = IO.pipe
    response_reader, response_writer = IO.pipe
    stderr_reader, stderr_writer = IO.pipe
    client = PiRpcClient.new(stdin: command_writer, stdout: response_reader, stderr: stderr_reader, request_timeout: 1)
    server = Thread.new do
      command = JSON.parse(command_reader.gets)
      stderr_writer.write("x" * (1024 * 1024))
      response_writer.puts JSON.generate(id: command.fetch("id"), type: "response", command: command.fetch("type"), success: true)
    end

    response = client.get_state

    assert_equal true, response.fetch("success")
  ensure
    server&.kill
    command_reader&.close
    command_writer&.close
    response_reader&.close
    response_writer&.close
    stderr_reader&.close
    stderr_writer&.close
  end

  def test_times_out_when_pi_process_does_not_respond
    command_reader, command_writer = IO.pipe
    response_reader, response_writer = IO.pipe
    client = PiRpcClient.new(stdin: command_writer, stdout: response_reader, request_timeout: 0.05)

    request_thread = Thread.new { client.get_state }
    request_thread.report_on_exception = false
    Timeout.timeout(1) { command_reader.gets }

    error = assert_raises(PiRpcClient::RequestTimeout) { Timeout.timeout(1) { request_thread.value } }
    assert_includes error.message, "get_state"
  ensure
    request_thread&.kill
    command_reader&.close
    command_writer&.close
    response_reader&.close
    response_writer&.close
  end

  def test_abort_uses_shorter_timeout
    command_reader, command_writer = IO.pipe
    response_reader, response_writer = IO.pipe
    client = PiRpcClient.new(stdin: command_writer, stdout: response_reader, request_timeout: 1, abort_timeout: 0.05)

    abort_thread = Thread.new { client.abort }
    abort_thread.report_on_exception = false
    Timeout.timeout(1) { command_reader.gets }

    assert_raises(PiRpcClient::RequestTimeout) { Timeout.timeout(1) { abort_thread.value } }
  ensure
    abort_thread&.kill
    command_reader&.close
    command_writer&.close
    response_reader&.close
    response_writer&.close
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

    assert_equal({ event_sequence: 4, event_replay_cursor: 0, active_tool_events: [JSON.parse(JSON.generate(latest_update))] }, client.live_snapshot)

    writer.puts JSON.generate({ type: "tool_execution_end", toolCallId: "call-1", toolName: "subagent", result: { content: [{ type: "text", text: "done" }] } })
    writer.puts JSON.generate({ id: "state-2", type: "state" })
    client.request("get_state", id: "state-2")

    assert_equal({ event_sequence: 5, event_replay_cursor: 0, active_tool_events: [] }, client.live_snapshot)
  ensure
    writer&.close
    reader&.close
  end

  def test_snapshots_latest_native_message_queues_with_order_and_duplicates
    input = StringIO.new
    reader, writer = IO.pipe
    client = PiRpcClient.new(stdin: input, stdout: reader)

    writer.puts JSON.generate({ type: "queue_update", steering: ["First", "First"], followUp: ["Later"] })
    writer.puts JSON.generate({ id: "state-1", type: "state" })
    client.request("get_state", id: "state-1")

    assert_equal({ "steering" => ["First", "First"], "followUp" => ["Later"] }, client.live_snapshot.fetch(:queued_messages))

    writer.puts JSON.generate({ type: "queue_update", steering: [], followUp: [] })
    writer.puts JSON.generate({ id: "state-2", type: "state" })
    client.request("get_state", id: "state-2")

    refute client.live_snapshot.key?(:queued_messages)
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

    assert_equal({ event_sequence: 2, event_replay_cursor: 0, active_tool_events: [] }, client.live_snapshot)
  ensure
    writer&.close
    reader&.close
  end

  def test_bounds_large_running_tool_snapshots_without_discarding_latest_progress
    input = StringIO.new
    reader, writer = IO.pipe
    client = PiRpcClient.new(stdin: input, stdout: reader)
    request_thread = Thread.new { client.request("get_state", id: "state-1") }
    Timeout.timeout(1) { sleep 0.01 until input.string.include?("get_state") }

    writer.puts JSON.generate({
      type: "tool_execution_update",
      toolCallId: "call-1",
      toolName: "subagent",
      partialResult: {
        content: [{ type: "text", text: "Latest meaningful progress" }],
        details: {
          task: "Inspect the project #{"😀" * 20_000}",
          status: "running",
          tools: 20.times.map do |index|
            { name: "read", args: { path: "/tmp/file-#{index}" }, status: "done", output: "x" * 10_000 }
          end,
          textItems: ["Latest meaningful progress"],
          streamingText: "",
          usage: { turns: 20 },
          model: "provider/model"
        }
      }
    })
    writer.puts JSON.generate({ id: "state-1", type: "state" })
    request_thread.join(1)

    event = client.live_snapshot.fetch(:active_tool_events).first
    details = event.dig("partialResult", "details")
    assert_operator JSON.generate(event).bytesize, :<=, PiRpcClient::MAX_ACTIVE_TOOL_SNAPSHOT_BYTES
    assert JSON.generate(event).valid_encoding?
    assert_equal "running", details["status"]
    assert_equal "Latest meaningful progress", details["textItems"].last
    assert_equal "read", details["tools"].last["name"]
    refute_equal "Subagent is still running…", event.dig("partialResult", "content", 0, "text")
  ensure
    request_thread&.kill
    writer&.close
    reader&.close
  end

  def test_falls_back_to_latest_text_when_compacted_progress_still_exceeds_the_limit
    input = StringIO.new
    reader, writer = IO.pipe
    client = PiRpcClient.new(stdin: input, stdout: reader)
    request_thread = Thread.new { client.request("get_state", id: "state-1") }
    Timeout.timeout(1) { sleep 0.01 until input.string.include?("get_state") }
    arguments = 12.times.to_h { |index| ["argument-#{index}", "x" * 2_000] }

    writer.puts JSON.generate({
      type: "tool_execution_update",
      toolCallId: "call-1",
      toolName: "subagent",
      partialResult: {
        content: [{ type: "text", text: "Latest aggregate progress" }],
        details: {
          status: "running",
          tools: 10.times.map { { name: "custom", args: arguments, status: "done", output: "x" * 2_000 } },
          textItems: ["Latest aggregate progress"],
          usage: { turns: 10 }
        }
      }
    })
    writer.puts JSON.generate({ id: "state-1", type: "state" })
    request_thread.join(1)

    event = client.live_snapshot.fetch(:active_tool_events).first
    assert_operator JSON.generate(event).bytesize, :<=, PiRpcClient::MAX_ACTIVE_TOOL_SNAPSHOT_BYTES
    assert_nil event.dig("partialResult", "details")
    assert_equal "Latest aggregate progress", event.dig("partialResult", "content", 0, "text")
  ensure
    request_thread&.kill
    writer&.close
    reader&.close
  end

  def test_keeps_latest_text_when_large_subagent_details_have_an_unknown_shape
    input = StringIO.new
    reader, writer = IO.pipe
    client = PiRpcClient.new(stdin: input, stdout: reader)
    request_thread = Thread.new { client.request("get_state", id: "state-1") }
    Timeout.timeout(1) { sleep 0.01 until input.string.include?("get_state") }

    writer.puts JSON.generate({
      type: "tool_execution_update",
      toolCallId: "call-1",
      toolName: "subagent",
      partialResult: {
        content: [{ type: "text", text: "Latest progress" }],
        details: { customProgress: "x" * PiRpcClient::MAX_ACTIVE_TOOL_SNAPSHOT_BYTES }
      }
    })
    writer.puts JSON.generate({ id: "state-1", type: "state" })
    request_thread.join(1)

    event = client.live_snapshot.fetch(:active_tool_events).first
    assert_operator JSON.generate(event).bytesize, :<=, PiRpcClient::MAX_ACTIVE_TOOL_SNAPSHOT_BYTES
    assert_equal "Latest progress", event.dig("partialResult", "content", 0, "text")
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

  def test_samples_rapid_oversized_tool_updates
    now = -1
    input = StringIO.new
    output = StringIO.new([
      JSON.generate(oversized_tool_update("first")),
      JSON.generate(oversized_tool_update("second")),
      JSON.generate({ id: "state-1", type: "state" })
    ].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output, monotonic_clock: -> { now += 1 })

    client.request("get_state", id: "state-1")

    replay = client.events_after(0)
    assert_equal 1, replay.fetch(:last_seq)
    assert_equal "first", replay.dig(:events, 0, "partialResult", "content", 0, "text")
  end

  def test_samples_each_oversized_tool_call_independently
    input = StringIO.new
    output = StringIO.new([
      JSON.generate(oversized_tool_update("first one", tool_call_id: "call-1")),
      JSON.generate(oversized_tool_update("first two", tool_call_id: "call-2")),
      JSON.generate(oversized_tool_update("later one", tool_call_id: "call-1")),
      JSON.generate(oversized_tool_update("later two", tool_call_id: "call-2")),
      JSON.generate({ id: "state-1", type: "state" })
    ].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output, monotonic_clock: -> { 0 })

    client.request("get_state", id: "state-1")

    replay = client.events_after(0)
    assert_equal 2, replay.fetch(:last_seq)
    assert_equal ["first one", "first two"], replay.fetch(:events).map { |event| event.dig("partialResult", "content", 0, "text") }
  end

  def test_samples_another_oversized_update_after_the_interval
    now = -2
    input = StringIO.new
    output = StringIO.new([
      JSON.generate(oversized_tool_update("first")),
      JSON.generate(oversized_tool_update("latest")),
      JSON.generate({ id: "state-1", type: "state" })
    ].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output, monotonic_clock: -> { now += 2 })

    client.request("get_state", id: "state-1")

    replay = client.events_after(0)
    assert_equal 2, replay.fetch(:last_seq)
    assert_equal "latest", replay.dig(:events, 0, "partialResult", "content", 0, "text")
  end

  def test_preserves_complete_tool_end_after_sampling_progress
    update = oversized_tool_update("progress")
    final_text = "complete" * (PiRpcClient::RPC_READ_CHUNK_BYTES / 8)
    input = StringIO.new
    output = StringIO.new([
      JSON.generate(update),
      JSON.generate(update),
      JSON.generate({ type: "tool_execution_end", toolCallId: "call-1", toolName: "subagent", result: { content: [{ type: "text", text: final_text }] } }),
      JSON.generate({ id: "state-1", type: "state" })
    ].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output, monotonic_clock: -> { 0 })

    client.request("get_state", id: "state-1")

    replay = client.events_after(0)
    assert_equal ["tool_execution_end"], replay.fetch(:events).map { |event| event.fetch("type") }
    assert_equal final_text, replay.dig(:events, 0, "result", "content", 0, "text")
  end

  def test_preserves_oversized_command_responses
    payload = "x" * PiRpcClient::RPC_READ_CHUNK_BYTES
    input = StringIO.new
    output = StringIO.new(JSON.generate({ id: "state-1", type: "state", data: payload }) + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    response = client.request("get_state", id: "state-1")

    assert_equal payload, response.fetch("data")
  end

  def test_discards_sampled_tool_updates_above_the_hard_limit
    update = oversized_tool_update("progress", payload_bytes: PiRpcClient::MAX_SAMPLED_TOOL_UPDATE_BYTES)
    final_text = "complete"
    input = StringIO.new
    output = StringIO.new([
      JSON.generate(update),
      JSON.generate({ type: "tool_execution_end", toolCallId: "call-1", toolName: "subagent", result: { content: [{ type: "text", text: final_text }] } }),
      JSON.generate({ id: "state-1", type: "state" })
    ].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    client.request("get_state", id: "state-1")

    replay = client.events_after(0)
    assert_equal 1, replay.fetch(:last_seq)
    assert_equal final_text, replay.dig(:events, 0, "result", "content", 0, "text")
  end

  def test_samples_oversized_updates_with_heavily_escaped_bounded_ids
    escaped_id = "\n" * 600
    input = StringIO.new
    output = StringIO.new([
      JSON.generate(oversized_tool_update("progress", tool_call_id: escaped_id)),
      JSON.generate({ id: "state-1", type: "state" })
    ].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    client.request("get_state", id: "state-1")

    replay = client.events_after(0)
    assert_equal 1, replay.fetch(:last_seq)
    assert_equal escaped_id, replay.dig(:events, 0, "toolCallId")
  end

  def test_handles_utf8_split_across_rpc_read_chunks
    emoji = "😀"
    emoji_offset = JSON.generate(oversized_tool_update(emoji)).b.index(emoji.b)
    text = ("x" * (PiRpcClient::RPC_READ_CHUNK_BYTES - 1 - emoji_offset)) + emoji
    line = JSON.generate(oversized_tool_update(text))
    assert_equal PiRpcClient::RPC_READ_CHUNK_BYTES - 1, line.b.index(emoji.b)
    input = StringIO.new
    output = StringIO.new([line, JSON.generate({ id: "state-1", type: "state" })].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    client.request("get_state", id: "state-1")

    assert_includes client.events_after(0).dig(:events, 0, "partialResult", "content", 0, "text"), emoji
  end

  def test_falls_back_to_parsing_oversized_updates_with_unrecognized_key_order
    update = ->(text) do
      {
        toolCallId: "call-1",
        type: "tool_execution_update",
        toolName: "subagent",
        partialResult: {
          content: [{ type: "text", text: text }],
          details: { cumulativeState: "x" * PiRpcClient::RPC_READ_CHUNK_BYTES }
        }
      }
    end
    input = StringIO.new
    output = StringIO.new([
      JSON.generate(update.call("first")),
      JSON.generate(update.call("latest")),
      JSON.generate({ id: "state-1", type: "state" })
    ].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output, monotonic_clock: -> { 0 })

    client.request("get_state", id: "state-1")

    replay = client.events_after(0)
    assert_equal 2, replay.fetch(:last_seq)
    assert_equal "latest", replay.dig(:events, 0, "partialResult", "content", 0, "text")
  end

  def test_bounds_sampling_with_more_active_tool_calls_than_tracking_slots
    updates = 2.times.flat_map do
      (PiRpcClient::MAX_OVERSIZED_TOOL_UPDATE_SAMPLE_KEYS + 1).times.map do |index|
        JSON.generate(oversized_tool_update("progress", tool_call_id: "call-#{index}"))
      end
    end
    input = StringIO.new
    output = StringIO.new((updates + [JSON.generate({ id: "state-1", type: "state" })]).join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output, monotonic_clock: -> { 0 })

    client.request("get_state", id: "state-1")

    replay = client.events_after(0)
    assert_equal PiRpcClient::MAX_OVERSIZED_TOOL_UPDATE_SAMPLE_KEYS, replay.fetch(:last_seq)
    assert_equal PiRpcClient::MAX_OVERSIZED_TOOL_UPDATE_SAMPLE_KEYS, replay.fetch(:events).length
  end

  def test_bounds_the_number_of_parsed_oversized_updates_in_a_long_stream
    now = -20
    update = oversized_tool_update("progress")
    input = StringIO.new
    output = StringIO.new((Array.new(100, JSON.generate(update)) + [JSON.generate({ id: "state-1", type: "state" })]).join("\n") + "\n")
    client = PiRpcClient.new(
      stdin: input,
      stdout: output,
      monotonic_clock: -> { now += 1 },
      oversized_tool_update_sample_interval_seconds: 20,
      request_timeout: nil
    )

    client.request("get_state", id: "state-1")

    assert_equal 5, client.events_after(0).fetch(:last_seq)
  end

  def test_coalesces_cumulative_tool_updates_and_discards_them_when_the_tool_finishes
    input = StringIO.new
    output = StringIO.new([
      JSON.generate({ type: "tool_execution_start", toolCallId: "call-1", toolName: "subagent", args: {} }),
      JSON.generate({ type: "tool_execution_update", toolCallId: "call-1", toolName: "subagent", partialResult: { content: [{ type: "text", text: "first" }] } }),
      JSON.generate({ type: "tool_execution_update", toolCallId: "call-1", toolName: "subagent", partialResult: { content: [{ type: "text", text: "latest" }] } }),
      JSON.generate({ id: "state-1", type: "state" })
    ].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    client.request("get_state", id: "state-1")

    replay = client.events_after(0)
    assert_equal ["tool_execution_start", "tool_execution_update"], replay.fetch(:events).map { |event| event.fetch("type") }
    assert_equal "latest", replay.dig(:events, 1, "partialResult", "content", 0, "text")
    assert_equal ["latest"], client.events_after(2).fetch(:events).map { |event| event.dig("partialResult", "content", 0, "text") }
    assert_equal 3, replay.fetch(:last_seq)

    reader, writer = IO.pipe
    client = PiRpcClient.new(stdin: StringIO.new, stdout: reader)
    writer.puts JSON.generate({ type: "tool_execution_start", toolCallId: "call-1", toolName: "subagent", args: {} })
    writer.puts JSON.generate({ type: "tool_execution_update", toolCallId: "call-1", toolName: "subagent", partialResult: { content: [{ type: "text", text: "progress" }] } })
    writer.puts JSON.generate({ type: "tool_execution_end", toolCallId: "call-1", toolName: "subagent", result: { content: [{ type: "text", text: "done" }] } })
    writer.puts JSON.generate({ id: "state-2", type: "state" })
    client.request("get_state", id: "state-2")

    assert_equal ["tool_execution_start", "tool_execution_end"], client.events_after(0).fetch(:events).map { |event| event.fetch("type") }
  ensure
    writer&.close
    reader&.close
  end

  def test_coalesces_cumulative_message_updates_and_discards_them_when_the_message_finishes
    message = ->(text) { { role: "assistant", content: [{ type: "text", text: text }] } }
    input = StringIO.new
    output = StringIO.new([
      JSON.generate({ type: "message_start", message: message.call("") }),
      JSON.generate({ type: "message_update", message: message.call("first") }),
      JSON.generate({ type: "message_update", message: message.call("latest") }),
      JSON.generate({ type: "message_end", message: message.call("done") }),
      JSON.generate({ id: "state-1", type: "state" })
    ].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    client.request("get_state", id: "state-1")

    replay = client.events_after(0)
    assert_equal ["message_start", "message_end"], replay.fetch(:events).map { |event| event.fetch("type") }
    assert_equal "done", replay.dig(:events, 1, "message", "content", 0, "text")
    assert_equal 4, replay.fetch(:last_seq)
  end

  def test_compacts_oversized_subagent_updates_in_the_replay_buffer
    input = StringIO.new
    output = StringIO.new([
      JSON.generate({
        type: "tool_execution_update",
        toolCallId: "call-1",
        toolName: "subagent",
        partialResult: {
          content: [{ type: "text", text: "Latest progress" }],
          details: { customProgress: "x" * PiRpcClient::MAX_ACTIVE_TOOL_SNAPSHOT_BYTES }
        }
      }),
      JSON.generate({ id: "state-1", type: "state" })
    ].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    client.request("get_state", id: "state-1")

    event = client.events_after(0).fetch(:events).first
    assert_operator JSON.generate(event).bytesize, :<=, PiRpcClient::MAX_ACTIVE_TOOL_SNAPSHOT_BYTES
    assert_equal "Latest progress", event.dig("partialResult", "content", 0, "text")
    assert_nil event.dig("partialResult", "details")
  end

  def test_discards_an_oversized_subagent_update_with_an_untrackable_tool_call_id
    oversized_id = "x" * (PiRpcClient::MAX_ACTIVE_TOOL_SNAPSHOT_BYTES + 1)
    input = StringIO.new
    output = StringIO.new([
      JSON.generate({
        type: "tool_execution_update",
        toolCallId: oversized_id,
        toolName: "subagent",
        partialResult: { content: [{ type: "text", text: "Latest progress" }] }
      }),
      JSON.generate({ id: "state-1", type: "state" })
    ].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    client.request("get_state", id: "state-1")

    assert_equal 0, client.event_replay_cursor
    assert_equal({ events: [], last_seq: 0, missed: false }, client.events_after(0))
  end

  def test_reports_missed_events_when_byte_budget_discards_replay
    first = { type: "event", name: "one", payload: "x" * 40 }
    second = { type: "event", name: "two", payload: "y" * 40 }
    input = StringIO.new
    output = StringIO.new([
      JSON.generate(first),
      JSON.generate(second),
      JSON.generate({ id: "state-1", type: "state" })
    ].join("\n") + "\n")
    client = PiRpcClient.new(
      stdin: input,
      stdout: output,
      event_buffer_bytes: JSON.generate(second).bytesize + 1
    )

    client.request("get_state", id: "state-1")

    assert_equal 1, client.event_replay_cursor
    assert_equal 1, client.live_snapshot.fetch(:event_replay_cursor)
    assert_equal({ events: [], last_seq: 2, missed: true }, client.events_after(0))
    assert_equal ["two"], client.events_after(client.event_replay_cursor).fetch(:events).map { |event| event.fetch("name") }
  end

  def test_reports_missed_events_when_one_event_exceeds_the_byte_budget
    input = StringIO.new
    output = StringIO.new([
      JSON.generate({ type: "event", payload: "x" * 100 }),
      JSON.generate({ id: "state-1", type: "state" })
    ].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output, event_buffer_bytes: 10)

    client.request("get_state", id: "state-1")

    assert_equal 1, client.event_replay_cursor
    assert_equal({ events: [], last_seq: 1, missed: true }, client.events_after(0))
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

    assert_equal 1, client.event_replay_cursor
    assert_equal({ events: [], last_seq: 2, missed: true }, client.events_after(0))
    assert_equal({ events: [{ "type" => "event", "name" => "two" }], last_seq: 2, missed: false }, client.events_after(client.event_replay_cursor))
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
    writer.puts JSON.generate({ type: "agent_end", willRetry: true })
    writer.puts JSON.generate({ id: "state-3", type: "state" })
    client.request("get_state", id: "state-3")
    assert client.busy?
    assert_equal Time.at(1_000), client.busy_since
    assert client.agent_running?

    now = Time.at(1_015)
    writer.puts JSON.generate({ type: "agent_settled" })
    writer.puts JSON.generate({ id: "state-4", type: "state" })
    client.request("get_state", id: "state-4")
    refute client.busy?
    assert_nil client.busy_since
    refute client.agent_running?
    assert_equal Time.at(1_015), client.settled_at
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
    assert_equal true, client.live_snapshot[:compacting]
    assert_equal Time.at(1_000), client.live_snapshot[:compacting_since]
    assert_equal 1_000_000, client.events_after(0).fetch(:events).first["gatewayTimestamp"]

    writer.puts JSON.generate({ type: "compaction" })
    writer.puts JSON.generate({ id: "state-2", type: "state" })
    client.request("get_state", id: "state-2")

    refute client.busy?
    refute client.compacting?
    assert_nil client.busy_since
    refute client.live_snapshot.key?(:compacting)
    refute client.live_snapshot.key?(:compacting_since)
  ensure
    writer&.close
    reader&.close
  end

  def test_live_snapshot_tracks_compaction_start_separately_from_agent_start
    now = Time.at(1_000)
    input = StringIO.new
    reader, writer = IO.pipe
    client = PiRpcClient.new(stdin: input, stdout: reader, clock: -> { now })

    writer.puts JSON.generate({ type: "agent_start" })
    writer.puts JSON.generate({ id: "state-1", type: "state" })
    client.request("get_state", id: "state-1")

    now = Time.at(1_005)
    writer.puts JSON.generate({ type: "compaction_start" })
    writer.puts JSON.generate({ id: "state-2", type: "state" })
    client.request("get_state", id: "state-2")

    snapshot = client.live_snapshot
    assert_equal Time.at(1_000), snapshot[:busy_since]
    assert_equal Time.at(1_005), snapshot[:compacting_since]
    assert_equal [1_000_000, 1_005_000], client.events_after(0).fetch(:events).map { |event| event["gatewayTimestamp"] }
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
    refute client.compacting?
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

  def test_queues_follow_up_during_compaction_and_prompts_after_compaction_finishes
    input = StringIO.new
    reader, writer = IO.pipe
    client = PiRpcClient.new(stdin: input, stdout: reader)

    writer.puts JSON.generate({ type: "compaction_start" })
    writer.puts JSON.generate({ id: "state-1", type: "state" })
    client.request("get_state", id: "state-1")

    response = client.follow_up("Run after compaction")

    assert_equal true, response.fetch("success")
    refute_includes input.string, "follow_up"

    writer.puts JSON.generate({ type: "compaction_end" })
    Timeout.timeout(1) { sleep 0.01 until input.string.include?("Run after compaction") }

    queued_command = input.string.lines.map { |line| JSON.parse(line) }.find { |command| command["message"] == "Run after compaction" }
    assert_equal "prompt", queued_command.fetch("type")
  ensure
    writer&.close
    reader&.close
  end

  def test_flushes_first_compaction_follow_up_as_prompt_and_remaining_as_follow_ups
    input = StringIO.new
    reader, writer = IO.pipe
    client = PiRpcClient.new(stdin: input, stdout: reader)
    image = { "type" => "image", "source" => { "type" => "base64", "media_type" => "image/png", "data" => "abc" } }

    writer.puts JSON.generate({ type: "compaction_start" })
    writer.puts JSON.generate({ id: "state-1", type: "state" })
    client.request("get_state", id: "state-1")
    client.follow_up("First", [image])
    client.follow_up("Second")

    writer.puts JSON.generate({ type: "compaction_end" })
    Timeout.timeout(1) { sleep 0.01 until input.string.include?("Second") }

    queued_commands = input.string.lines.map { |line| JSON.parse(line) }.select { |command| ["First", "Second"].include?(command["message"]) }
    assert_equal ["prompt", "follow_up"], queued_commands.map { |command| command.fetch("type") }
    assert_equal [image], queued_commands.first.fetch("images")
  ensure
    writer&.close
    reader&.close
  end

  def test_flushes_compaction_follow_ups_as_native_follow_ups_when_pi_will_retry
    input = StringIO.new
    reader, writer = IO.pipe
    client = PiRpcClient.new(stdin: input, stdout: reader)

    writer.puts JSON.generate({ type: "compaction_start" })
    writer.puts JSON.generate({ id: "state-1", type: "state" })
    client.request("get_state", id: "state-1")
    client.follow_up("First")
    client.follow_up("Second")

    writer.puts JSON.generate({ type: "compaction_end", willRetry: true })
    Timeout.timeout(1) { sleep 0.01 until input.string.include?("Second") }

    queued_commands = input.string.lines.map { |line| JSON.parse(line) }.select { |command| ["First", "Second"].include?(command["message"]) }
    assert_equal ["follow_up", "follow_up"], queued_commands.map { |command| command.fetch("type") }
  ensure
    writer&.close
    reader&.close
  end

  def test_surfaces_failed_deferred_compaction_command_response_as_event
    input = StringIO.new
    reader, writer = IO.pipe
    client = PiRpcClient.new(stdin: input, stdout: reader)

    writer.puts JSON.generate({ type: "compaction_start" })
    writer.puts JSON.generate({ id: "state-1", type: "state" })
    client.request("get_state", id: "state-1")
    client.follow_up("Queued message")
    writer.puts JSON.generate({ type: "compaction_end", willRetry: true })
    Timeout.timeout(1) { sleep 0.01 until input.string.include?("Queued message") }

    queued_command = input.string.lines.map { |line| JSON.parse(line) }.find { |command| command["message"] == "Queued message" }
    failed_response = { id: queued_command.fetch("id"), type: "response", command: "follow_up", success: false, error: "Prompt rejected" }
    writer.puts JSON.generate(failed_response)

    events = Timeout.timeout(1) do
      loop do
        events = client.events_after(0).fetch(:events)
        break events if events.any? { |event| event["error"] == "Prompt rejected" }
        sleep 0.01
      end
    end
    assert_includes events, JSON.parse(JSON.generate(failed_response))
  ensure
    writer&.close
    reader&.close
  end

  def test_keeps_follow_ups_queued_while_compaction_queue_is_flushing
    input = StringIO.new
    output = StringIO.new
    client = PiRpcClient.new(stdin: input, stdout: output)
    client.instance_variable_set(:@flushing_compaction_follow_ups, true)

    response = client.follow_up("Queued during flush")
    client.send(:flush_compaction_follow_ups, [{ message: "First queued" }])

    assert_equal true, response.fetch("success")
    queued_commands = input.string.lines.map { |line| JSON.parse(line) }
    assert_equal ["prompt", "follow_up"], queued_commands.map { |command| command.fetch("type") }
    assert_equal ["First queued", "Queued during flush"], queued_commands.map { |command| command.fetch("message") }
  end

  def test_waits_to_send_new_prompts_until_compaction_follow_ups_finish_flushing
    input = StringIO.new
    reader, writer = IO.pipe
    client = PiRpcClient.new(stdin: input, stdout: reader)
    client.instance_variable_set(:@flushing_compaction_follow_ups, true)

    prompt_thread = Thread.new { client.prompt("New prompt") rescue nil }
    sleep 0.05
    refute_includes input.string, "New prompt"

    client.send(:flush_compaction_follow_ups, [{ message: "First queued" }])
    Timeout.timeout(1) { sleep 0.01 until input.string.include?("New prompt") }

    queued_commands = input.string.lines.map { |line| JSON.parse(line) }
    assert_equal ["First queued", "New prompt"], queued_commands.map { |command| command.fetch("message") }
  ensure
    prompt_thread&.kill
    writer&.close
    reader&.close
  end

  def test_waits_to_send_new_prompts_when_compaction_has_queued_follow_ups
    input = StringIO.new
    reader, writer = IO.pipe
    client = PiRpcClient.new(stdin: input, stdout: reader)

    writer.puts JSON.generate({ type: "compaction_start" })
    writer.puts JSON.generate({ id: "state-1", type: "state" })
    client.request("get_state", id: "state-1")
    client.follow_up("Queued follow-up")

    prompt_thread = Thread.new { client.prompt("New prompt") rescue nil }
    sleep 0.05
    refute_includes input.string, "New prompt"

    writer.puts JSON.generate({ type: "compaction_end" })
    Timeout.timeout(1) { sleep 0.01 until input.string.include?("New prompt") }

    queued_commands = input.string.lines.map { |line| JSON.parse(line) }.select { |command| ["Queued follow-up", "New prompt"].include?(command["message"]) }
    assert_equal ["Queued follow-up", "New prompt"], queued_commands.map { |command| command.fetch("message") }
  ensure
    prompt_thread&.kill
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

  def test_extension_ui_dialogs_are_buffered_until_the_web_client_responds
    command_reader, command_writer = IO.pipe
    response_reader, response_writer = IO.pipe
    client = PiRpcClient.new(stdin: command_writer, stdout: response_reader)

    %w[select confirm input editor].each_with_index do |method, index|
      response_writer.puts JSON.generate({ type: "extension_ui_request", id: "dialog-#{index}", method: method })
    end
    fire_and_forget_methods = %w[notify setStatus setWidget setTitle set_editor_text]
    fire_and_forget_methods.each_with_index do |method, index|
      response_writer.puts JSON.generate({ type: "extension_ui_request", id: "notice-#{index}", method: method })
    end
    Timeout.timeout(1) do
      sleep 0.01 until client.events_after(0).fetch(:events).length == 9
    end
    refute IO.select([command_reader], nil, nil, 0.05), "extension UI requests should not receive automatic responses"

    assert_equal({ "type" => "response", "command" => "extension_ui_response", "success" => true }, client.extension_ui_response("dialog-0", value: "Allow"))
    assert_equal({ "type" => "extension_ui_response", "id" => "dialog-0", "value" => "Allow" }, JSON.parse(Timeout.timeout(1) { command_reader.gets }))
    refute_includes client.live_snapshot.fetch(:extension_ui).fetch(:pending_dialogs).map { |dialog| dialog.fetch("id") }, "dialog-0"

    client.extension_ui_response("dialog-1", confirmed: false)
    assert_equal({ "type" => "extension_ui_response", "id" => "dialog-1", "confirmed" => false }, JSON.parse(Timeout.timeout(1) { command_reader.gets }))

    client.extension_ui_response("dialog-2", cancelled: true)
    assert_equal({ "type" => "extension_ui_response", "id" => "dialog-2", "cancelled" => true }, JSON.parse(Timeout.timeout(1) { command_reader.gets }))

    events = client.events_after(0).fetch(:events)
    assert_equal ["editor"] + fire_and_forget_methods, events.map { |event| event["method"] }
  ensure
    command_reader&.close
    command_writer&.close
    response_reader&.close
    response_writer&.close
  end

  def test_live_snapshot_tracks_extension_ui_state_and_removes_cleared_values
    input = StringIO.new
    reader, writer = IO.pipe
    now = 10.0
    client = PiRpcClient.new(stdin: input, stdout: reader, monotonic_clock: -> { now })

    events = [
      { type: "extension_ui_request", id: "dialog-1", method: "input", message: "Name?", timeout: 5_000 },
      { type: "extension_ui_request", id: "dialog-2", method: "confirm", message: "Continue?" },
      { type: "extension_ui_request", method: "setStatus", statusKey: "branch", statusText: "Ready" },
      { type: "extension_ui_request", method: "setWidget", widgetKey: "summary", widgetLines: ["One"], widgetPlacement: "belowEditor" },
      { type: "extension_ui_request", method: "setTitle", title: "Extension title" }
    ]
    events.each { |event| writer.puts JSON.generate(event) }
    Timeout.timeout(1) { sleep 0.01 until client.events_after(0).fetch(:events).length == events.length }

    now = 12.0
    state = client.live_snapshot.fetch(:extension_ui)
    assert_equal ["dialog-1", "dialog-2"], state.fetch(:pending_dialogs).map { |dialog| dialog.fetch("id") }
    assert_equal 3_000, state.fetch(:pending_dialogs).first.fetch("timeout")
    assert_equal [{ "type" => "extension_ui_request", "method" => "setStatus", "statusKey" => "branch", "statusText" => "Ready" }], state.fetch(:statuses)
    assert_equal "summary", state.fetch(:widgets).first.fetch("widgetKey")
    assert_equal({ "type" => "extension_ui_request", "method" => "setTitle", "title" => "Extension title" }, state.fetch(:title))

    writer.puts JSON.generate({ type: "extension_ui_request", method: "setStatus", statusKey: "branch", statusText: nil })
    writer.puts JSON.generate({ type: "extension_ui_request", method: "setWidget", widgetKey: "summary" })
    writer.puts JSON.generate({ type: "extension_ui_request", method: "setTitle", title: nil })
    Timeout.timeout(1) { sleep 0.01 until client.events_after(0).fetch(:events).length == events.length + 3 }

    state = client.live_snapshot.fetch(:extension_ui)
    assert_empty state.fetch(:statuses)
    assert_empty state.fetch(:widgets)
    assert_nil state.fetch(:title)
  ensure
    writer&.close
    reader&.close
  end

  def test_expired_extension_ui_dialogs_are_not_restored_or_answered
    input = StringIO.new
    reader, writer = IO.pipe
    now = 20.0
    client = PiRpcClient.new(stdin: input, stdout: reader, monotonic_clock: -> { now })

    writer.puts JSON.generate({ type: "extension_ui_request", id: "dialog-1", method: "select", options: ["Yes"], timeout: 1_000 })
    Timeout.timeout(1) { sleep 0.01 until client.events_after(0).fetch(:events).any? }
    now = 21.0

    assert_empty client.live_snapshot.fetch(:extension_ui, {}).fetch(:pending_dialogs, [])
    response = client.extension_ui_response("dialog-1", value: "Yes")
    assert_equal false, response.fetch("success")
    assert_empty input.string
  ensure
    writer&.close
    reader&.close
  end

  def test_events_after_prunes_expired_extension_ui_dialogs
    input = StringIO.new
    reader, writer = IO.pipe
    now = 20.0
    client = PiRpcClient.new(stdin: input, stdout: reader, monotonic_clock: -> { now })

    writer.puts JSON.generate({ type: "extension_ui_request", id: "dialog-1", method: "confirm", timeout: 1_000 })
    Timeout.timeout(1) { sleep 0.01 until client.events_after(0).fetch(:events).any? }
    now = 21.0
    assert_empty client.events_after(0).fetch(:events)
    now = 20.5

    assert_equal false, client.extension_ui_response("dialog-1", confirmed: true).fetch("success")
    assert_empty input.string
  ensure
    writer&.close
    reader&.close
  end

  def test_events_after_does_not_replay_an_answered_extension_ui_dialog
    command_reader, command_writer = IO.pipe
    response_reader, response_writer = IO.pipe
    client = PiRpcClient.new(stdin: command_writer, stdout: response_reader)

    response_writer.puts JSON.generate({ type: "extension_ui_request", id: "dialog-1", method: "confirm" })
    Timeout.timeout(1) { sleep 0.01 until client.events_after(0).fetch(:events).any? }
    client.extension_ui_response("dialog-1", confirmed: true)

    assert_empty client.events_after(0).fetch(:events)
    assert_equal "dialog-1", JSON.parse(Timeout.timeout(1) { command_reader.gets }).fetch("id")
  ensure
    command_reader&.close
    command_writer&.close
    response_reader&.close
    response_writer&.close
  end

  def test_events_after_reports_the_remaining_dialog_timeout
    input = StringIO.new
    reader, writer = IO.pipe
    now = 20.0
    client = PiRpcClient.new(stdin: input, stdout: reader, monotonic_clock: -> { now })

    writer.puts JSON.generate({ type: "extension_ui_request", id: "dialog-1", method: "confirm", timeout: 2_000 })
    Timeout.timeout(1) { sleep 0.01 until client.events_after(0).fetch(:events).any? }
    now = 20.5

    event = client.events_after(0).fetch(:events).first
    assert_equal 1_500, event.fetch("timeout")
    refute event.key?("gatewayExpiresAt")
  ensure
    writer&.close
    reader&.close
  end

  def test_recording_extension_ui_state_prunes_expired_dialogs
    input = StringIO.new
    reader, writer = IO.pipe
    now = 20.0
    client = PiRpcClient.new(stdin: input, stdout: reader, monotonic_clock: -> { now })

    writer.puts JSON.generate({ type: "extension_ui_request", id: "dialog-1", method: "confirm", timeout: 1_000 })
    Timeout.timeout(1) { sleep 0.01 until client.events_after(0).fetch(:events).any? }
    now = 21.0
    writer.puts JSON.generate({ type: "extension_ui_request", method: "setStatus", statusKey: "review", statusText: "Ready" })
    Timeout.timeout(1) { sleep 0.01 until client.event_sequence == 2 }
    now = 20.5

    assert_equal false, client.extension_ui_response("dialog-1", confirmed: true).fetch("success")
    assert_empty input.string
  ensure
    writer&.close
    reader&.close
  end

  def test_extension_ui_dialog_is_not_replayed_while_its_response_is_being_written
    write_started = Queue.new
    release_write = Queue.new
    input = Object.new
    input.define_singleton_method(:write) do |_command|
      write_started << true
      release_write.pop
    end
    input.define_singleton_method(:flush) {}
    reader, writer = IO.pipe
    client = PiRpcClient.new(stdin: input, stdout: reader)

    writer.puts JSON.generate({ type: "extension_ui_request", id: "dialog-1", method: "confirm" })
    Timeout.timeout(1) { sleep 0.01 until client.events_after(0).fetch(:events).any? }
    response_thread = Thread.new { client.extension_ui_response("dialog-1", confirmed: true) }
    write_started.pop

    assert_empty client.events_after(0).fetch(:events)
    assert_empty client.live_snapshot.fetch(:extension_ui).fetch(:pending_dialogs)

    release_write << true
    assert_equal true, response_thread.value.fetch("success")
  ensure
    release_write << true if release_write&.empty?
    response_thread&.join(1)
    writer&.close
    reader&.close
  end

  def test_extension_ui_response_removes_pending_dialog_only_after_write_succeeds
    command_reader, command_writer = IO.pipe
    response_reader, response_writer = IO.pipe
    client = PiRpcClient.new(stdin: command_writer, stdout: response_reader)

    response_writer.puts JSON.generate({ type: "extension_ui_request", id: "dialog-1", method: "confirm" })
    initial_events = Timeout.timeout(1) do
      loop do
        events = client.events_after(0)
        break events if events.fetch(:events).any?
        sleep 0.01
      end
    end
    command_reader.close

    assert_raises(Errno::EPIPE) { client.extension_ui_response("dialog-1", confirmed: true) }
    assert_equal ["dialog-1"], client.live_snapshot.fetch(:extension_ui).fetch(:pending_dialogs).map { |dialog| dialog.fetch("id") }
    restored_events = client.events_after(initial_events.fetch(:last_seq)).fetch(:events)
    assert_equal ["dialog-1"], restored_events.map { |event| event.fetch("id") }
  ensure
    command_reader&.close unless command_reader&.closed?
    command_writer&.close
    response_reader&.close
    response_writer&.close
  end

  def test_extension_ui_response_times_out_when_pi_stdin_is_full
    command_reader, command_writer = IO.pipe
    response_reader, response_writer = IO.pipe
    client = PiRpcClient.new(stdin: command_writer, stdout: response_reader, request_timeout: 0.05)
    response_writer.puts JSON.generate({ type: "extension_ui_request", id: "dialog-1", method: "confirm" })
    Timeout.timeout(1) { sleep 0.01 until client.events_after(0).fetch(:events).any? }
    begin
      loop { command_writer.write_nonblock("x" * 4096) }
    rescue IO::WaitWritable
      nil
    end

    assert_raises(PiRpcClient::RequestTimeout) do
      client.extension_ui_response("dialog-1", confirmed: true)
    end
    assert_equal ["dialog-1"], client.live_snapshot.fetch(:extension_ui).fetch(:pending_dialogs).map { |dialog| dialog.fetch("id") }
  ensure
    command_reader&.close
    command_writer&.close
    response_reader&.close
    response_writer&.close
  end

  def test_reads_bounded_tree_snapshot_through_extension_bridge
    input = StringIO.new
    snapshot = {
      entries: [{ entryId: "user-1", parentId: nil, depth: 0, type: "message", role: "user", text: "Prompt", timestamp: "2026-06-13T10:00:00Z", current: true, latest: true }],
      leafId: "user-1",
      truncated: false,
      totalEntries: 1,
      filter: "default",
      settings: { treeFilterMode: "default", branchSummary: { skipPrompt: false } }
    }
    output = StringIO.new([
      JSON.generate({ type: "extension_ui_request", method: "setStatus", statusKey: "gripi_tree_snapshot:abc123", statusText: JSON.generate({ ok: true }.merge(snapshot)) }),
      JSON.generate({ id: "prompt-1", type: "response", command: "prompt", success: true })
    ].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    response = with_secure_random_hex("abc123") { client.tree_snapshot }

    assert_equal "user-1", response.dig("data", "leafId")
    assert_equal "Prompt", response.dig("data", "entries", 0, "text")
    assert_equal "default", response.dig("data", "settings", "treeFilterMode")
    command = JSON.parse(input.string)
    _request_id, payload = decode_extension_command(command.fetch("message"), "gripi_tree_snapshot")
    assert_equal({}, payload)
    assert_empty client.events_after(0).fetch(:events)
  end

  def test_reads_current_tree_leaf_through_lightweight_extension_bridge
    input = StringIO.new
    output = StringIO.new([
      JSON.generate({ type: "extension_ui_request", method: "setStatus", statusKey: "gripi_tree_leaf:abc123", statusText: JSON.generate(ok: true, leafId: "assistant-1") }),
      JSON.generate({ id: "prompt-1", type: "response", command: "prompt", success: true })
    ].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    response = with_secure_random_hex("abc123") { client.tree_leaf }

    assert_equal "assistant-1", response.dig("data", "leafId")
    command = JSON.parse(input.string)
    decode_extension_command(command.fetch("message"), "gripi_tree_leaf")
    assert_empty client.events_after(0).fetch(:events)
  end

  def test_tree_bridge_times_out_and_discards_late_rpc_and_status_responses
    command_reader, command_writer = IO.pipe
    response_reader, response_writer = IO.pipe
    client = PiRpcClient.new(stdin: command_writer, stdout: response_reader, tree_bridge_timeout: 0.05)

    response = with_secure_random_hex("abc123") { client.tree_snapshot }
    command = JSON.parse(Timeout.timeout(1) { command_reader.gets })

    assert_equal false, response.fetch("success")
    assert_equal "Session tree request timed out", response.fetch("error")

    response_writer.puts JSON.generate({ id: command.fetch("id"), type: "response", command: "prompt", success: true })
    response_writer.puts JSON.generate({ type: "extension_ui_request", method: "setStatus", statusKey: "gripi_tree_snapshot:abc123", statusText: JSON.generate(ok: true, entries: []) })
    Timeout.timeout(1) { sleep 0.01 until client.events_after(0).fetch(:last_seq).zero? }

    state_thread = Thread.new { client.get_state }
    state_command = JSON.parse(Timeout.timeout(1) { command_reader.gets })
    response_writer.puts JSON.generate({ id: state_command.fetch("id"), type: "response", command: "get_state", success: true })

    assert_equal true, Timeout.timeout(1) { state_thread.value }.fetch("success")
    assert_empty client.events_after(0).fetch(:events)
  ensure
    state_thread&.kill
    command_reader&.close
    command_writer&.close
    response_reader&.close
    response_writer&.close
  end

  def test_tree_bridge_rejects_responses_stored_after_its_deadline
    monotonic_time = 0
    input = StringIO.new
    input.define_singleton_method(:write) do |value|
      written = super(value)
      sleep 0.05
      monotonic_time = 6
      written
    end
    output = StringIO.new([
      JSON.generate({ type: "extension_ui_request", method: "setStatus", statusKey: "gripi_tree_leaf:abc123", statusText: JSON.generate(ok: true, leafId: "late") }),
      JSON.generate({ id: "prompt-1", type: "response", command: "prompt", success: true })
    ].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output, monotonic_clock: -> { monotonic_time })

    response = with_secure_random_hex("abc123") { client.tree_leaf }

    assert_equal false, response.fetch("success")
    assert_equal "Session tree request timed out", response.fetch("error")
    assert_empty client.events_after(0).fetch(:events)
  end

  def test_tree_bridge_times_out_while_waiting_for_extension_status
    command_reader, command_writer = IO.pipe
    response_reader, response_writer = IO.pipe
    client = PiRpcClient.new(stdin: command_writer, stdout: response_reader, tree_bridge_timeout: 0.05)

    response_thread = Thread.new { with_secure_random_hex("abc123") { client.tree_leaf } }
    command = JSON.parse(Timeout.timeout(1) { command_reader.gets })
    response_writer.puts JSON.generate({ id: command.fetch("id"), type: "response", command: "prompt", success: true })
    response = Timeout.timeout(1) { response_thread.value }

    assert_equal false, response.fetch("success")
    assert_equal "Session tree request timed out", response.fetch("error")
    response_writer.puts JSON.generate({ type: "extension_ui_request", method: "setStatus", statusKey: "gripi_tree_leaf:abc123", statusText: JSON.generate(ok: true, leafId: "late") })
    sleep 0.05
    assert_empty client.events_after(0).fetch(:events)
  ensure
    response_thread&.kill
    command_reader&.close
    command_writer&.close
    response_reader&.close
    response_writer&.close
  end

  def test_navigate_tree_sends_structured_options_through_extension_bridge
    input = StringIO.new
    output = StringIO.new([
      JSON.generate({ type: "extension_ui_request", method: "setStatus", statusKey: "gripi_tree_navigate:abc123", statusText: JSON.generate(ok: true, cancelled: false, editorText: "Complete original prompt") }),
      JSON.generate({ id: "prompt-1", type: "response", command: "prompt", success: true })
    ].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    response = with_secure_random_hex("abc123") do
      client.navigate_tree("entry-2", summary: "custom", custom_instructions: "Focus on tests")
    end

    assert_equal false, response.dig("data", "cancelled")
    assert_equal "Complete original prompt", response.dig("data", "editorText")
    command = JSON.parse(input.string)
    request_id, payload = decode_extension_command(command.fetch("message"), "gripi_tree_navigate")
    assert_equal "abc123", request_id
    assert_equal({ "entryId" => "entry-2", "summary" => "custom", "customInstructions" => "Focus on tests" }, payload)
  end

  def test_tree_snapshot_sends_explicit_filter_through_extension_bridge
    input = StringIO.new
    output = StringIO.new([
      JSON.generate({ type: "extension_ui_request", method: "setStatus", statusKey: "gripi_tree_snapshot:abc123", statusText: JSON.generate(ok: true, entries: [], leafId: nil, filter: "user-only", settings: {}) }),
      JSON.generate({ id: "prompt-1", type: "response", command: "prompt", success: true })
    ].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    response = with_secure_random_hex("abc123") { client.tree_snapshot("user-only") }

    assert_equal "user-only", response.dig("data", "filter")
    command = JSON.parse(input.string)
    _request_id, payload = decode_extension_command(command.fetch("message"), "gripi_tree_snapshot")
    assert_equal({ "filter" => "user-only" }, payload)
  end

  def test_sets_and_clears_native_labels_through_extension_bridge
    commands = []
    response_reader, response_writer = IO.pipe
    input = Object.new
    input.define_singleton_method(:write) do |raw|
      command = JSON.parse(raw)
      commands << command
      _command_name, request_id, encoded_payload = command.fetch("message").split(" ", 3)
      payload = JSON.parse(Base64.urlsafe_decode64(encoded_payload))
      response_writer.puts JSON.generate(type: "extension_ui_request", method: "setStatus", statusKey: "gripi_tree_label:#{request_id}", statusText: JSON.generate(ok: true, entryId: payload.fetch("entryId"), label: payload["label"]))
      response_writer.puts JSON.generate(id: command.fetch("id"), type: "response", command: "prompt", success: true)
      raw.bytesize
    end
    input.define_singleton_method(:flush) {}
    client = PiRpcClient.new(stdin: input, stdout: response_reader)

    set_response = with_secure_random_hex("abc123") { client.set_tree_label("entry-2", " checkpoint ") }
    clear_response = with_secure_random_hex("def456") { client.set_tree_label("entry-2", nil) }

    assert_equal({ "entryId" => "entry-2", "label" => "checkpoint" }, decode_extension_command(commands[0].fetch("message"), "gripi_tree_label").last)
    assert_equal({ "entryId" => "entry-2", "label" => nil }, decode_extension_command(commands[1].fetch("message"), "gripi_tree_label").last)
    assert_equal({ "entryId" => "entry-2", "label" => "checkpoint" }, set_response.fetch("data"))
    assert_equal({ "entryId" => "entry-2", "label" => nil }, clear_response.fetch("data"))
  ensure
    response_reader&.close
    response_writer&.close
  end

  def test_extension_bridge_rejects_a_json_response_with_the_wrong_shape
    input = StringIO.new
    output = StringIO.new([
      JSON.generate({ type: "extension_ui_request", method: "setStatus", statusKey: "gripi_tree_snapshot:abc123", statusText: JSON.generate([]) }),
      JSON.generate({ id: "prompt-1", type: "response", command: "prompt", success: true })
    ].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    response = with_secure_random_hex("abc123") { client.tree_snapshot }

    assert_equal false, response.fetch("success")
    assert_equal "Extension command returned an invalid response", response.fetch("error")
  end

  def test_extension_bridge_reports_structured_failure
    input = StringIO.new
    output = StringIO.new([
      JSON.generate({ type: "extension_ui_request", method: "setStatus", statusKey: "gripi_tree_navigate:abc123", statusText: JSON.generate(ok: false, error: "Session is busy") }),
      JSON.generate({ id: "prompt-1", type: "response", command: "prompt", success: true })
    ].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    response = with_secure_random_hex("abc123") { client.navigate_tree("entry-2") }

    assert_equal false, response.fetch("success")
    assert_equal "Session is busy", response.fetch("error")
  end

  def test_command_helpers_send_supported_rpc_commands
    commands = []
    response_reader, response_writer = IO.pipe
    input = Object.new
    input.define_singleton_method(:write) do |payload|
      command = JSON.parse(payload)
      commands << command
      message = command["message"].to_s
      if message.start_with?("/gripi_tree_")
        name, request_id = message.split(" ", 3)
        response_writer.puts JSON.generate({ type: "extension_ui_request", method: "setStatus", statusKey: "#{name.delete_prefix("/")}:#{request_id}", statusText: JSON.generate(ok: true) })
      end
      response_writer.puts JSON.generate({ id: command.fetch("id"), type: "response", command: command.fetch("type"), success: true, data: {} })
      payload.bytesize
    end
    input.define_singleton_method(:flush) {}
    client = PiRpcClient.new(stdin: input, stdout: response_reader)

    client.get_state
    client.get_messages
    with_secure_random_hex("tree123") { client.tree_snapshot }
    with_secure_random_hex("leaf123") { client.tree_leaf }
    client.prompt("Hello", [{ type: "image", data: "abc", mimeType: "image/png" }])
    client.steer("Redirect now", [{ type: "image", data: "steer", mimeType: "image/webp" }])
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
    client.follow_up("After done", [{ type: "image", data: "def", mimeType: "image/jpeg" }])
    client.get_available_models
    client.set_model("anthropic", "claude-sonnet-4")
    client.set_thinking_level("high")
    client.cycle_thinking_level

    assert_equal [
      { "id" => "get_state-1", "type" => "get_state" },
      { "id" => "get_messages-2", "type" => "get_messages" },
      { "id" => "prompt-3", "type" => "prompt", "message" => "/gripi_tree_snapshot tree123 e30" },
      { "id" => "prompt-4", "type" => "prompt", "message" => "/gripi_tree_leaf leaf123 e30" },
      { "id" => "prompt-5", "type" => "prompt", "message" => "Hello", "images" => [{ "type" => "image", "data" => "abc", "mimeType" => "image/png" }] },
      { "id" => "steer-6", "type" => "steer", "message" => "Redirect now", "images" => [{ "type" => "image", "data" => "steer", "mimeType" => "image/webp" }] },
      { "id" => "abort-7", "type" => "abort" },
      { "id" => "new_session-8", "type" => "new_session", "parentSession" => "/tmp/session.jsonl" },
      { "id" => "switch_session-9", "type" => "switch_session", "sessionPath" => "/tmp/other-session.jsonl" },
      { "id" => "get_commands-10", "type" => "get_commands" },
      { "id" => "compact-11", "type" => "compact", "customInstructions" => "Focus summary" },
      { "id" => "set_session_name-12", "type" => "set_session_name", "name" => "Useful name" },
      { "id" => "get_fork_messages-13", "type" => "get_fork_messages" },
      { "id" => "fork-14", "type" => "fork", "entryId" => "entry-1" },
      { "id" => "clone-15", "type" => "clone" },
      { "id" => "prompt-16", "type" => "prompt", "message" => "/gripi_tree_navigate abc123 #{Base64.urlsafe_encode64(JSON.generate(entryId: "entry-2", summary: "none"), padding: false)}" },
      { "id" => "follow_up-17", "type" => "follow_up", "message" => "After done", "images" => [{ "type" => "image", "data" => "def", "mimeType" => "image/jpeg" }] },
      { "id" => "get_available_models-18", "type" => "get_available_models" },
      { "id" => "set_model-19", "type" => "set_model", "provider" => "anthropic", "modelId" => "claude-sonnet-4" },
      { "id" => "set_thinking_level-20", "type" => "set_thinking_level", "level" => "high" },
      { "id" => "cycle_thinking_level-21", "type" => "cycle_thinking_level" }
    ], commands
  ensure
    response_reader&.close
    response_writer&.close
  end

  private

  def oversized_tool_update(text, tool_call_id: "call-1", payload_bytes: PiRpcClient::RPC_READ_CHUNK_BYTES)
    {
      type: "tool_execution_update",
      toolCallId: tool_call_id,
      toolName: "subagent",
      partialResult: {
        content: [{ type: "text", text: text }],
        details: { cumulativeState: "x" * payload_bytes }
      }
    }
  end

  def process_running?(pid)
    return false unless pid&.positive?

    stat_path = "/proc/#{pid}/stat"
    return File.read(stat_path).split.fetch(2) != "Z" if File.exist?(stat_path)

    Process.kill(0, pid)
    true
  rescue Errno::ENOENT, Errno::ESRCH
    false
  end

  def decode_extension_command(message, name)
    command, request_id, encoded_payload = message.split(" ", 3)
    assert_equal "/#{name}", command
    [request_id, JSON.parse(Base64.urlsafe_decode64(encoded_payload))]
  end

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
