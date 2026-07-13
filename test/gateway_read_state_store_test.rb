require "minitest/autorun"
require "json"
require "tmpdir"
require_relative "../lib/gateway_read_state_store"

class GatewayReadStateStoreTest < Minitest::Test
  Session = Struct.new(:path, :assistant_response_count)

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
