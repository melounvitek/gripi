# frozen_string_literal: true

require "minitest/autorun"
require "fileutils"
require "tmpdir"
require_relative "../lib/rpc/pending_session_registry"
require_relative "../lib/rpc/start_new_session"
require_relative "../lib/rpc/branch_session"

class RpcSessionWorkflowsTest < Minitest::Test
  def test_start_new_session_registers_persisted_session
    Dir.mktmpdir do |dir|
      session_path = File.join(dir, "new-session.jsonl")
      FileUtils.touch(session_path)
      calls = []
      rpc_clients = FakeRpcClientRegistry.new
      pending_sessions = Rpc::PendingSessionRegistry.new

      client = FakeRpcClient.new(calls, session_path)

      result = Rpc::StartNewSession.call(
        dir,
        client_factory: ->(cwd) { calls << [:start_new, cwd]; client },
        rpc_clients: rpc_clients,
        pending_sessions: pending_sessions,
        sessions_root: dir
      )

      assert_equal session_path, result
      assert_same client, rpc_clients.client_for(result)
      assert_empty pending_sessions.paths
      assert_equal [[:start_new, dir], [:get_state]], calls
    end
  end

  def test_start_new_session_remembers_pending_session_when_file_is_missing
    Dir.mktmpdir do |dir|
      cwd = File.join(dir, "project")
      FileUtils.mkdir_p(cwd)
      calls = []
      rpc_clients = FakeRpcClientRegistry.new
      pending_sessions = Rpc::PendingSessionRegistry.new

      client = FakeRpcClient.new(calls)

      result = Rpc::StartNewSession.call(
        cwd,
        client_factory: ->(started_cwd) { calls << [:start_new, started_cwd]; client },
        rpc_clients: rpc_clients,
        pending_sessions: pending_sessions,
        sessions_root: dir
      )

      assert_match %r{\A#{Regexp.escape(dir)}/pending-[^/]+\.jsonl\z}, result
      assert_same client, rpc_clients.client_for(result)
      assert_equal cwd, pending_sessions.cwd_for(result)
      assert_equal [[:start_new, cwd], [:get_state]], calls
    end
  end

  def test_branch_session_moves_client_to_new_session_path
    Dir.mktmpdir do |dir|
      previous_path = File.join(dir, "session.jsonl")
      new_path = File.join(dir, "branched.jsonl")
      FileUtils.touch(new_path)
      calls = []
      client = FakeRpcClient.new(calls, new_path)
      rpc_clients = FakeRpcClientRegistry.new
      rpc_clients.register(previous_path, client)
      pending_sessions = Rpc::PendingSessionRegistry.new

      result = Rpc::BranchSession.call(
        previous_path,
        rpc_clients: rpc_clients,
        pending_sessions: pending_sessions,
        cwd: dir
      )

      assert_equal new_path, result
      assert_same client, rpc_clients.client_for(new_path)
      refute rpc_clients.active?(previous_path)
      assert_empty pending_sessions.paths
      assert_equal [[:get_state]], calls
    end
  end

  def test_branch_session_remembers_pending_path_when_file_is_missing
    Dir.mktmpdir do |dir|
      previous_path = File.join(dir, "session.jsonl")
      pending_path = File.join(dir, "pending-branch.jsonl")
      calls = []
      client = FakeRpcClient.new(calls, pending_path)
      rpc_clients = FakeRpcClientRegistry.new
      rpc_clients.register(previous_path, client)
      pending_sessions = Rpc::PendingSessionRegistry.new

      result = Rpc::BranchSession.call(
        previous_path,
        rpc_clients: rpc_clients,
        pending_sessions: pending_sessions,
        cwd: dir
      )

      assert_equal pending_path, result
      assert_same client, rpc_clients.client_for(pending_path)
      assert_equal dir, pending_sessions.cwd_for(pending_path)
    end
  end

  class FakeRpcClient
    def initialize(calls, session_file = nil)
      @calls = calls
      @session_file = session_file
    end

    def get_state
      @calls << [:get_state]
      { "type" => "response", "data" => { "sessionFile" => @session_file } }
    end
  end

  class FakeRpcClientRegistry
    def initialize
      @clients = {}
    end

    def register(session_path, client)
      @clients[session_path] = client
    end

    def client_for(session_path)
      @clients[session_path]
    end

    def move(from_path, to_path)
      @clients[to_path] = @clients.delete(from_path)
    end

    def active?(session_path)
      @clients.key?(session_path)
    end
  end
end
