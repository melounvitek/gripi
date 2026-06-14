require "minitest/autorun"
require "stringio"
require "json"
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
    assert_equal [["pi", "--mode", "rpc", "--session", "/tmp/session.jsonl"]], calls
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
    assert_equal [["pi", "--mode", "rpc", { chdir: "/tmp/project" }]], calls
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

  def test_includes_payload_fields_in_command
    input = StringIO.new
    output = StringIO.new(JSON.generate({ id: "prompt-1", type: "accepted" }) + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    client.request("prompt", id: "prompt-1", message: "Hello", images: [{ type: "image", data: "abc", mimeType: "image/png" }])

    assert_equal({ "id" => "prompt-1", "type" => "prompt", "message" => "Hello", "images" => [{ "type" => "image", "data" => "abc", "mimeType" => "image/png" }] }, JSON.parse(input.string))
  end

  def test_command_helpers_send_supported_rpc_commands
    input = StringIO.new
    output = StringIO.new([
      JSON.generate({ id: "get_state-1", type: "state" }),
      JSON.generate({ id: "get_messages-2", type: "messages" }),
      JSON.generate({ id: "prompt-3", type: "accepted" }),
      JSON.generate({ id: "abort-4", type: "aborted" }),
      JSON.generate({ id: "new_session-5", type: "response", command: "new_session", success: true, data: { cancelled: false } }),
      JSON.generate({ id: "switch_session-6", type: "response", command: "switch_session", success: true, data: { cancelled: false } }),
      JSON.generate({ id: "get_commands-7", type: "response", command: "get_commands", success: true, data: { commands: [] } }),
      JSON.generate({ id: "compact-8", type: "response", command: "compact", success: true, data: {} }),
      JSON.generate({ id: "set_session_name-9", type: "response", command: "set_session_name", success: true })
    ].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    client.get_state
    client.get_messages
    client.prompt("Hello", [{ type: "image", data: "abc", mimeType: "image/png" }])
    client.abort
    client.new_session("/tmp/session.jsonl")
    client.switch_session("/tmp/other-session.jsonl")
    client.get_commands
    client.compact("Focus summary")
    client.set_session_name("Useful name")

    assert_equal [
      { "id" => "get_state-1", "type" => "get_state" },
      { "id" => "get_messages-2", "type" => "get_messages" },
      { "id" => "prompt-3", "type" => "prompt", "message" => "Hello", "images" => [{ "type" => "image", "data" => "abc", "mimeType" => "image/png" }] },
      { "id" => "abort-4", "type" => "abort" },
      { "id" => "new_session-5", "type" => "new_session", "parentSession" => "/tmp/session.jsonl" },
      { "id" => "switch_session-6", "type" => "switch_session", "sessionPath" => "/tmp/other-session.jsonl" },
      { "id" => "get_commands-7", "type" => "get_commands" },
      { "id" => "compact-8", "type" => "compact", "customInstructions" => "Focus summary" },
      { "id" => "set_session_name-9", "type" => "set_session_name", "name" => "Useful name" }
    ], input.string.lines.map { |line| JSON.parse(line) }
  end
end
