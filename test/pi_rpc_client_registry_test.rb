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

  def test_runs_close_callback_before_closing_the_client
    now = Time.at(1_000)
    calls = []
    registry = PiRpcClientRegistry.new(factory: ->(_session_path) { FakeClient.new(calls) }, clock: -> { now })
    registry.ensure_client("/tmp/session.jsonl")

    now = Time.at(2_801)
    registry.close_idle_clients(idle_timeout: 1_800, on_close: ->(path) { calls << [:on_close, path] })

    assert_equal [[:on_close, "/tmp/session.jsonl"], :close], calls
  end

  def test_keeps_busy_clients_past_idle_timeout
    now = Time.at(1_000)
    calls = []
    registry = PiRpcClientRegistry.new(factory: ->(_session_path) { FakeClient.new(calls) }, clock: -> { now })
    client = registry.ensure_client("/tmp/session.jsonl")
    client.busy = true

    now = Time.at(2_801)
    closed = registry.close_idle_clients(idle_timeout: 1_800)

    assert_empty closed
    assert registry.active?("/tmp/session.jsonl")
    assert_empty calls
  end

  def test_keeps_settled_client_until_idle_timeout_after_settlement
    now = Time.at(1_000)
    calls = []
    registry = PiRpcClientRegistry.new(factory: ->(_session_path) { FakeClient.new(calls) }, clock: -> { now })
    client = registry.ensure_client("/tmp/session.jsonl")
    client.settled_at = Time.at(2_800)

    now = Time.at(2_801)
    assert_empty registry.close_idle_clients(idle_timeout: 1_800)
    assert registry.active?("/tmp/session.jsonl")

    now = Time.at(4_601)
    assert_equal ["/tmp/session.jsonl"], registry.close_idle_clients(idle_timeout: 1_800)
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

  def test_touch_keeps_an_existing_client_alive_without_creating_one
    now = Time.at(1_000)
    calls = []
    registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" }, clock: -> { now })
    registry.register("/tmp/session.jsonl", FakeClient.new(calls))

    now = Time.at(2_000)
    assert registry.touch("/tmp/session.jsonl")
    refute registry.touch("/tmp/missing.jsonl")
    now = Time.at(3_799)

    assert_empty registry.close_idle_clients(idle_timeout: 1_800)
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

  def test_event_polling_does_not_keep_idle_rpc_client_alive
    now = Time.at(1_000)
    calls = []
    registry = PiRpcClientRegistry.new(factory: ->(_session_path) { FakeClient.new(calls) }, clock: -> { now })
    registry.ensure_client("/tmp/session.jsonl")

    now = Time.at(2_801)
    registry.events_after("/tmp/session.jsonl", 0)
    closed = registry.close_idle_clients(idle_timeout: 1_800)

    assert_equal ["/tmp/session.jsonl"], closed
    assert_equal [[:events_after, 0], :close], calls
  end

  def test_closes_specific_idle_client_but_not_busy_client
    calls = []
    registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
    client = FakeClient.new(calls)
    registry.register("/tmp/session.jsonl", client)

    client.busy = true
    refute registry.close_client_if_idle("/tmp/session.jsonl")
    assert registry.active?("/tmp/session.jsonl")

    client.busy = false
    assert registry.close_client_if_idle("/tmp/session.jsonl")
    refute registry.active?("/tmp/session.jsonl")
    assert_equal [:close], calls
  end

  def test_reports_client_busy_state
    calls = []
    registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
    client = FakeClient.new(calls)
    registry.register("/tmp/session.jsonl", client)

    refute registry.busy?("/tmp/session.jsonl")
    assert_nil registry.busy_since("/tmp/session.jsonl")
    refute registry.agent_running?("/tmp/session.jsonl")
    client.busy = true
    client.busy_since = Time.at(1_000)
    client.agent_running = true
    assert registry.busy?("/tmp/session.jsonl")
    assert_equal Time.at(1_000), registry.busy_since("/tmp/session.jsonl")
    assert registry.agent_running?("/tmp/session.jsonl")
    refute registry.busy?("/tmp/other.jsonl")
    assert_nil registry.busy_since("/tmp/other.jsonl")
    refute registry.agent_running?("/tmp/other.jsonl")
  end

  def test_events_after_for_inactive_session_does_not_create_client
    calls = []
    registry = PiRpcClientRegistry.new(factory: ->(_session_path) { calls << [:start] })

    result = registry.events_after("/tmp/session.jsonl", 0)

    assert_equal({ events: [], last_seq: 0, missed: false }, result)
    assert_empty calls
  end

  def test_serializes_rpc_operations_for_the_same_session
    registry = PiRpcClientRegistry.new(factory: ->(_session_path) { FakeClient.new([]) })
    first_started = Queue.new
    release_first = Queue.new
    second_started = Queue.new

    first = Thread.new do
      registry.with_client("/tmp/session.jsonl") do
        first_started << true
        release_first.pop
      end
    end
    first_started.pop
    second = Thread.new do
      registry.with_client("/tmp/session.jsonl") { second_started << true }
    end

    refute second_started.pop(timeout: 0.05)
    release_first << true
    assert second_started.pop(timeout: 1)
  ensure
    release_first << true if first&.alive?
    first&.join
    second&.join
  end

  def test_allows_concurrent_rpc_operation_while_serialized_operation_is_running
    registry = PiRpcClientRegistry.new(factory: ->(_session_path) { FakeClient.new([]) })
    first_started = Queue.new
    release_first = Queue.new
    concurrent_started = Queue.new

    first = Thread.new do
      registry.with_client("/tmp/session.jsonl") do
        first_started << true
        release_first.pop
      end
    end
    first_started.pop
    concurrent = Thread.new do
      registry.with_interrupt_client("/tmp/session.jsonl") { concurrent_started << true }
    end

    assert concurrent_started.pop(timeout: 1)
  ensure
    release_first << true if first&.alive?
    first&.join
    concurrent&.join
  end

  def test_evicts_client_after_terminal_io_failure_and_recreates_it_later
    calls = []
    clients = [FakeClient.new(calls), FakeClient.new(calls)]
    registry = PiRpcClientRegistry.new(factory: ->(_session_path) { clients.shift })

    first_client = registry.ensure_client("/tmp/session.jsonl")
    assert_raises(IOError) do
      registry.with_client("/tmp/session.jsonl") { raise IOError, "process exited" }
    end

    refute registry.active?("/tmp/session.jsonl")
    assert_equal [:close], calls
    refute_same first_client, registry.ensure_client("/tmp/session.jsonl")
  end

  def test_io_failure_does_not_evict_a_replacement_client
    calls = []
    registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
    original = FakeClient.new(calls)
    replacement = FakeClient.new(calls)
    registry.register("/tmp/session.jsonl", original)

    assert_raises(IOError) do
      registry.with_client("/tmp/session.jsonl") do
        registry.register("/tmp/session.jsonl", replacement)
        raise IOError, "original process exited"
      end
    end

    assert_same replacement, registry.client_for("/tmp/session.jsonl")
  end

  def test_keeps_clients_currently_in_use
    now = Time.at(1_000)
    calls = []
    registry = PiRpcClientRegistry.new(factory: ->(_session_path) { FakeClient.new(calls) }, clock: -> { now })
    registry.ensure_client("/tmp/session.jsonl")

    registry.with_active_client("/tmp/session.jsonl") do
      now = Time.at(2_801)
      closed = registry.close_idle_clients(idle_timeout: 1_800)

      assert_empty closed
      assert registry.active?("/tmp/session.jsonl")
      assert_empty calls
    end
  end

  class FakeClient
    attr_writer :busy
    attr_accessor :busy_since, :settled_at
    attr_writer :agent_running

    def initialize(calls)
      @calls = calls
      @busy = false
      @agent_running = false
    end

    def busy?
      @busy
    end

    def agent_running?
      @agent_running
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
