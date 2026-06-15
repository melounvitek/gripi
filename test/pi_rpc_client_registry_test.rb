require "minitest/autorun"
require_relative "../lib/pi_rpc_client_registry"

class PiRpcClientRegistryTest < Minitest::Test
  def test_closes_clients_idle_longer_than_timeout
    now = Time.at(1_000)
    calls = []
    registry = PiRpcClientRegistry.new(factory: ->(_session_path) { FakeClient.new(calls) }, clock: -> { now })
    registry.ensure_client("/tmp/session.jsonl")

    now = Time.at(2_801)
    closed = registry.close_idle_clients(idle_timeout: 1_800)

    assert_equal ["/tmp/session.jsonl"], closed
    refute registry.active?("/tmp/session.jsonl")
    assert_equal [:close], calls
  end

  def test_keeps_recent_clients
    now = Time.at(1_000)
    calls = []
    registry = PiRpcClientRegistry.new(factory: ->(_session_path) { FakeClient.new(calls) }, clock: -> { now })
    registry.ensure_client("/tmp/session.jsonl")

    now = Time.at(2_799)
    closed = registry.close_idle_clients(idle_timeout: 1_800)

    assert_empty closed
    assert registry.active?("/tmp/session.jsonl")
    assert_empty calls
  end

  def test_keeps_excepted_clients
    now = Time.at(1_000)
    calls = []
    registry = PiRpcClientRegistry.new(factory: ->(_session_path) { FakeClient.new(calls) }, clock: -> { now })
    registry.ensure_client("/tmp/pending.jsonl")

    now = Time.at(2_801)
    closed = registry.close_idle_clients(idle_timeout: 1_800, except: ["/tmp/pending.jsonl"])

    assert_empty closed
    assert registry.active?("/tmp/pending.jsonl")
    assert_empty calls
  end

  def test_returns_events_after_cursor_without_draining_for_other_consumers
    calls = []
    registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
    client = FakeClient.new(calls)
    registry.register("/tmp/session.jsonl", client)

    first = registry.events_after("/tmp/session.jsonl", 0)
    second = registry.events_after("/tmp/session.jsonl", 0)

    assert_equal({ events: [{ "type" => "event" }], last_seq: 1, missed: false }, first)
    assert_equal first, second
    assert_equal [[:events_after, 0], [:events_after, 0]], calls
  end

  def test_reports_client_busy_state
    calls = []
    registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
    client = FakeClient.new(calls)
    registry.register("/tmp/session.jsonl", client)

    refute registry.busy?("/tmp/session.jsonl")
    client.busy = true
    assert registry.busy?("/tmp/session.jsonl")
    refute registry.busy?("/tmp/other.jsonl")
  end

  def test_events_after_for_inactive_session_does_not_create_client
    calls = []
    registry = PiRpcClientRegistry.new(factory: ->(_session_path) { calls << [:start] })

    result = registry.events_after("/tmp/session.jsonl", 0)

    assert_equal({ events: [], last_seq: 0, missed: false }, result)
    assert_empty calls
  end

  def test_keeps_clients_currently_in_use
    now = Time.at(1_000)
    calls = []
    registry = PiRpcClientRegistry.new(factory: ->(_session_path) { FakeClient.new(calls) }, clock: -> { now })
    registry.ensure_client("/tmp/session.jsonl")

    registry.begin_use("/tmp/session.jsonl")
    now = Time.at(2_801)
    closed = registry.close_idle_clients(idle_timeout: 1_800)

    assert_empty closed
    assert registry.active?("/tmp/session.jsonl")
    assert_empty calls
  ensure
    registry.end_use("/tmp/session.jsonl") if registry
  end

  class FakeClient
    attr_writer :busy

    def initialize(calls)
      @calls = calls
      @busy = false
    end

    def busy?
      @busy
    end

    def events_after(after_seq)
      @calls << [:events_after, after_seq]
      { events: [{ "type" => "event" }], last_seq: 1, missed: false }
    end

    def close
      @calls << :close
    end
  end
end
