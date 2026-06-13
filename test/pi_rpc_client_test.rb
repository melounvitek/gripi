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

  def test_sends_jsonl_command_and_returns_matching_response
    input = StringIO.new
    output = StringIO.new(JSON.generate({ type: "event", name: "queued" }) + "\n" + JSON.generate({ id: "state-1", type: "state", cwd: "/tmp/project" }) + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    response = client.request("get_state", id: "state-1")

    assert_equal({ "id" => "state-1", "type" => "state", "cwd" => "/tmp/project" }, response)
    assert_equal [{ "type" => "event", "name" => "queued" }], client.drain_events
    assert_empty client.drain_events
    written = JSON.parse(input.string.lines.first)
    assert_equal({ "id" => "state-1", "type" => "get_state" }, written)
  end

  def test_includes_payload_fields_in_command
    input = StringIO.new
    output = StringIO.new(JSON.generate({ id: "prompt-1", type: "accepted" }) + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    client.request("prompt", id: "prompt-1", message: "Hello")

    assert_equal({ "id" => "prompt-1", "type" => "prompt", "message" => "Hello" }, JSON.parse(input.string))
  end

  def test_command_helpers_send_supported_rpc_commands
    input = StringIO.new
    output = StringIO.new([
      JSON.generate({ id: "get_state-1", type: "state" }),
      JSON.generate({ id: "get_messages-2", type: "messages" }),
      JSON.generate({ id: "prompt-3", type: "accepted" }),
      JSON.generate({ id: "abort-4", type: "aborted" })
    ].join("\n") + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    client.get_state
    client.get_messages
    client.prompt("Hello")
    client.abort

    assert_equal [
      { "id" => "get_state-1", "type" => "get_state" },
      { "id" => "get_messages-2", "type" => "get_messages" },
      { "id" => "prompt-3", "type" => "prompt", "message" => "Hello" },
      { "id" => "abort-4", "type" => "abort" }
    ], input.string.lines.map { |line| JSON.parse(line) }
  end
end
