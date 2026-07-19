require "minitest/autorun"
require "json"
require "stringio"
require_relative "../lib/rpc/diagnostics"
require_relative "../lib/pi_rpc_client"

class RpcDiagnosticsTest < Minitest::Test
  def test_emits_structured_metadata_when_enabled
    previous = ENV["GRIPI_RPC_DIAGNOSTICS"]
    ENV["GRIPI_RPC_DIAGNOSTICS"] = "1"

    _stdout, stderr = capture_io do
      Rpc::Diagnostics.log("command_started", command: "steer", rpc_id: "steer-1")
    end

    payload = JSON.parse(stderr)
    assert_equal "pi_rpc", payload.fetch("component")
    assert_equal "command_started", payload.fetch("event")
    assert_equal "steer", payload.fetch("command")
    assert_equal "steer-1", payload.fetch("rpc_id")
  ensure
    ENV["GRIPI_RPC_DIAGNOSTICS"] = previous
  end

  def test_rpc_diagnostics_do_not_include_prompt_contents
    previous = ENV["GRIPI_RPC_DIAGNOSTICS"]
    ENV["GRIPI_RPC_DIAGNOSTICS"] = "1"
    secret = "never-log-this-prompt"
    input = StringIO.new
    output = StringIO.new(JSON.generate(id: "prompt-1", type: "response", command: "prompt", success: true) + "\n")
    client = PiRpcClient.new(stdin: input, stdout: output)

    _stdout, stderr = capture_io { client.prompt(secret) }

    refute_includes stderr, secret
    assert_includes stderr, '"command":"prompt"'
    assert_includes stderr, '"rpc_id":"prompt-1"'
  ensure
    ENV["GRIPI_RPC_DIAGNOSTICS"] = previous
  end

  def test_is_silent_by_default
    previous = ENV.delete("GRIPI_RPC_DIAGNOSTICS")

    _stdout, stderr = capture_io { Rpc::Diagnostics.log("command_started", command: "prompt") }

    assert_empty stderr
  ensure
    ENV["GRIPI_RPC_DIAGNOSTICS"] = previous
  end
end
