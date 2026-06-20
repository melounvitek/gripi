require "minitest/autorun"
require_relative "../lib/rpc/pending_session_registry"

class RpcPendingSessionRegistryTest < Minitest::Test
  def test_tracks_pending_session_cwds
    registry = Rpc::PendingSessionRegistry.new

    registry.remember("/tmp/pending-1.jsonl", "/tmp/project-1")
    registry.remember("/tmp/pending-2.jsonl", "/tmp/project-2")

    assert_equal "/tmp/project-1", registry.cwd_for("/tmp/pending-1.jsonl")
    assert_equal ["/tmp/pending-1.jsonl", "/tmp/pending-2.jsonl"], registry.paths
    assert_equal [["/tmp/pending-1.jsonl", "/tmp/project-1"], ["/tmp/pending-2.jsonl", "/tmp/project-2"]], registry.entries

    registry.forget("/tmp/pending-1.jsonl")

    assert_nil registry.cwd_for("/tmp/pending-1.jsonl")
    assert_equal ["/tmp/pending-2.jsonl"], registry.paths
  end
end
