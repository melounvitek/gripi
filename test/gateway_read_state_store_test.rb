require "minitest/autorun"
require "json"
require "tmpdir"
require_relative "../lib/gateway_read_state_store"

class GatewayReadStateStoreTest < Minitest::Test
  Session = Struct.new(:path, :assistant_response_count)

  def test_reads_unread_paths_for_multiple_sessions_from_one_state_snapshot
    Dir.mktmpdir do |dir|
      state_path = File.join(dir, "read-state.json")
      first = Session.new("/tmp/first.jsonl", 3)
      second = Session.new("/tmp/second.jsonl", 4)
      File.write(state_path, JSON.generate(first.path => 2, second.path => 4))
      store = GatewayReadStateStore.new(path: state_path)
      reads = 0
      read_state = GatewayReadStateStore.instance_method(:read_state)
      store.define_singleton_method(:read_state) do
        reads += 1
        read_state.bind_call(self)
      end

      assert_equal [first.path], store.unread_paths([first, second])
      assert_equal 1, reads
    end
  end

  def test_observing_a_reduced_response_count_keeps_future_responses_detectable
    Dir.mktmpdir do |dir|
      state_path = File.join(dir, "read-state.json")
      session = Session.new("/tmp/session.jsonl", 2)
      File.write(state_path, JSON.generate(session.path => 5))
      store = GatewayReadStateStore.new(path: state_path)

      store.observe_sessions([session])

      assert_equal 2, JSON.parse(File.read(state_path)).fetch(session.path)

      session.assistant_response_count = 3
      assert store.unread?(session)
    end
  end
end
