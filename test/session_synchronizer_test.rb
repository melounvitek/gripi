require "minitest/autorun"
require "tmpdir"
require "json"
require_relative "../lib/pi_session_store"
require_relative "../lib/pi_rpc_client_registry"
require_relative "../lib/sessions/session_synchronizer"

class SessionSynchronizerTest < Minitest::Test
  FakeClient = Struct.new(:positions, :entry_batches, :busy, :closed, keyword_init: true) do
    def session_position(cursor)
      positions.fetch(cursor)
    end

    def session_entries_after(cursor)
      session_position(cursor).merge(entries: entry_batches&.fetch(cursor, []) || [])
    end

    def busy?
      !!busy
    end

    def close
      self.closed = true
    end
  end

  class HookClient
    attr_reader :closed

    def initialize(&hook)
      @hook = hook
      @closed = false
    end

    def session_position(cursor)
      @hook.call
      { known: true, leaf_id: cursor, error: nil }
    end

    def session_entries_after(cursor)
      session_position(cursor).merge(entries: [])
    end

    def busy?
      false
    end

    def close
      @closed = true
    end
  end

  class FakeRegistry
    attr_reader :client

    def initialize(client = nil, replacement: nil)
      @client = client
      @replacement = replacement
    end

    def active?(_path)
      !@client.nil?
    end

    def busy?(_path)
      @client&.busy? || false
    end

    def with_existing_client(_path, touch: true)
      yield @client if @client
    end

    def with_client(_path)
      @client ||= @replacement
      raise "missing client" unless @client

      yield @client
    end

    def with_interrupt_client(path, &block)
      with_client(path, &block)
    end

    def with_bash_client(path, &block)
      with_client(path, &block)
    end

    def with_existing_interrupt_client(_path)
      yield @client if @client
    end

    def close_client_if_idle(_path)
      return false unless @client && !@client.busy?

      @client.close
      @client = nil
      true
    end
  end

  def test_preserves_intentional_older_rpc_leaf
    with_session do |root, path|
      append(path, type: "message", id: "persisted", parentId: nil, message: { role: "user", content: [] })
      client = FakeClient.new(positions: {
        "persisted" => { known: true, leaf_id: "older", error: nil }
      })
      synchronizer = build_synchronizer(root, FakeRegistry.new(client))

      result = synchronizer.inspect(path)

      assert_equal :managed, result.mode
      assert_equal "older", result.rpc_leaf_id
      assert_equal "persisted", result.persisted_leaf_id
    end
  end

  def test_enters_external_follow_when_rpc_appends_and_exits_during_first_position_check
    with_session do |root, path|
      client = Object.new
      client.define_singleton_method(:session_position) do |_cursor|
        File.open(path, "a") { |file| file.puts(JSON.generate(type: "message", id: "external", parentId: nil, message: { role: "user", content: [] })) }
        raise IOError, "Pi RPC process exited"
      end
      registry = FakeRegistry.new(client)
      synchronizer = build_synchronizer(root, registry)

      result = synchronizer.inspect(path)

      assert_equal :external_follow, result.mode
      assert_equal "external", result.append_cursor
    end
  end

  def test_enters_external_follow_when_rpc_does_not_know_persisted_cursor
    with_session do |root, path|
      append(path, type: "message", id: "external", parentId: nil, message: { role: "user", content: [] })
      client = FakeClient.new(positions: {
        "external" => { known: false, leaf_id: nil, error: nil }
      })
      registry = FakeRegistry.new(client)
      synchronizer = build_synchronizer(root, registry)

      result = synchronizer.inspect(path)

      assert_equal :external_follow, result.mode
      assert_equal "external", result.append_cursor
      assert client.closed
      refute registry.active?(path)
    end
  end

  def test_detects_external_append_after_observing_session_without_rpc_client
    with_session do |root, path|
      registry = FakeRegistry.new
      synchronizer = build_synchronizer(root, registry)
      assert_equal :available, synchronizer.inspect(path).mode

      append(path, type: "message", id: "external", parentId: nil, message: { role: "user", content: [] })
      result = synchronizer.inspect(path)

      assert_equal :external_follow, result.mode
      assert_equal "external", result.append_cursor
    end
  end

  def test_accepts_gateway_append_known_to_active_rpc_client
    with_session do |root, path|
      positions = {
        nil => { known: true, leaf_id: nil, error: nil },
        "gateway" => { known: true, leaf_id: "gateway", error: nil }
      }
      client = FakeClient.new(
        positions: positions,
        entry_batches: { nil => [{ "id" => "gateway" }] }
      )
      registry = FakeRegistry.new(client)
      synchronizer = build_synchronizer(root, registry)
      synchronizer.inspect(path)

      append(path, type: "message", id: "gateway", parentId: nil, message: { role: "user", content: [] })
      positions[nil] = { known: true, leaf_id: "gateway", error: nil }
      result = synchronizer.inspect(path)

      assert_equal :managed, result.mode
      assert_equal "gateway", result.rpc_leaf_id
    end
  end

  def test_detects_unknown_pi_cli_entry_before_a_known_gateway_tail_entry
    with_session do |root, path|
      append(path, type: "message", id: "old", parentId: nil, message: { role: "user", content: [] })
      positions = { "old" => { known: true, leaf_id: "old", error: nil } }
      batches = {}
      client = FakeClient.new(positions: positions, entry_batches: batches)
      synchronizer = build_synchronizer(root, FakeRegistry.new(client))
      assert_equal :managed, synchronizer.inspect(path).mode

      append(path, type: "message", id: "pi-cli", parentId: "old", message: { role: "user", content: [] })
      append(path, type: "message", id: "gateway", parentId: "old", message: { role: "user", content: [] })
      positions["old"] = { known: true, leaf_id: "gateway", error: nil }
      batches["old"] = [{ "id" => "gateway" }]

      result = synchronizer.inspect(path)

      assert_equal :external_follow, result.mode
      assert client.closed
    end
  end

  def test_returns_to_available_when_managed_rpc_client_is_retired
    with_session do |root, path|
      append(path, type: "message", id: "persisted", parentId: nil, message: { role: "user", content: [] })
      registry = FakeRegistry.new(FakeClient.new(positions: {
        "persisted" => { known: true, leaf_id: "older", error: nil }
      }))
      synchronizer = build_synchronizer(root, registry)
      assert_equal :managed, synchronizer.inspect(path).mode
      registry.close_client_if_idle(path)

      result = synchronizer.inspect(path)

      assert_equal :available, result.mode
      assert_nil result.rpc_leaf_id
    end
  end

  def test_idle_reconciliation_does_not_yield_while_the_session_lock_is_held
    with_session do |root, path|
      append(path, type: "message", id: "persisted", parentId: nil, message: { role: "user", content: [] })
      client = FakeClient.new(positions: {
        "persisted" => { known: true, leaf_id: "persisted", error: nil }
      })
      synchronizer = build_synchronizer(root, FakeRegistry.new(client))
      lock = synchronizer.send(:lock_for, path)
      lock.lock
      yielded = false

      assert_nil synchronizer.reconcile_if_available(path) { yielded = true }
      refute yielded

      lock.unlock
      result = synchronizer.reconcile_if_available(path) { yielded = true }
      assert_equal :managed, result.mode
      assert yielded
    ensure
      lock&.unlock if lock&.owned?
    end
  end

  def test_idle_cleanup_reconciles_unseen_gateway_entries_before_closing_client
    with_session do |root, path|
      append(path, type: "message", id: "old", parentId: nil, message: { role: "user", content: [] })
      now = Time.at(1_000)
      positions = { "old" => { known: true, leaf_id: "old", error: nil } }
      batches = { "old" => [] }
      client = FakeClient.new(positions: positions, entry_batches: batches)
      registry = PiRpcClientRegistry.new(factory: ->(_path) { raise "unexpected start" }, clock: -> { now })
      registry.register(path, client)
      synchronizer = build_synchronizer(root, registry)
      assert_equal :managed, synchronizer.inspect(path).mode

      append(path, type: "message", id: "gateway", parentId: "old", message: { role: "assistant", content: [] })
      positions["old"] = { known: true, leaf_id: "gateway", error: nil }
      batches["old"] = [{ "id" => "gateway" }]
      now = Time.at(3_000)
      closed = registry.close_idle_clients(idle_timeout: 1_800) { |session_path| synchronizer.inspect(session_path) }

      assert_equal [path], closed
      assert_equal :available, synchronizer.inspect(path).mode
      refute_equal :external_follow, synchronizer.inspect(path).mode
    end
  end

  def test_fails_closed_when_session_has_incomplete_appended_data
    with_session do |root, path|
      synchronizer = build_synchronizer(root, FakeRegistry.new)
      synchronizer.inspect(path)
      File.open(path, "a") { |file| file.write("{\"type\":") }

      result = synchronizer.inspect(path)

      assert_equal :conflict, result.mode
      assert_includes result.error, "incomplete"
    end
  end

  def test_takeover_rejects_busy_stale_gateway_client
    with_session do |root, path|
      append(path, type: "message", id: "external", parentId: nil, message: { role: "user", content: [] })
      client = FakeClient.new(
        positions: { "external" => { known: false, leaf_id: nil, error: nil } },
        busy: true
      )
      synchronizer = build_synchronizer(root, FakeRegistry.new(client))
      assert_equal :external_follow, synchronizer.inspect(path).mode

      error = assert_raises(Sessions::SessionSynchronizer::BusyError) do
        synchronizer.take_over(path)
      end

      assert_includes error.message, "gateway task"
      refute client.closed
    end
  end

  def test_takeover_uses_fresh_rpc_at_persisted_leaf
    with_session do |root, path|
      append(path, type: "message", id: "external", parentId: nil, message: { role: "user", content: [] })
      stale = FakeClient.new(positions: { "external" => { known: false, leaf_id: nil, error: nil } })
      fresh = FakeClient.new(positions: { "external" => { known: true, leaf_id: "external", error: nil } })
      registry = FakeRegistry.new(stale, replacement: fresh)
      synchronizer = build_synchronizer(root, registry)
      assert_equal :external_follow, synchronizer.inspect(path).mode

      result = synchronizer.take_over(path)

      assert_equal :managed, result.mode
      assert_equal "external", result.rpc_leaf_id
      assert stale.closed
      refute fresh.closed
    end
  end

  def test_mutable_operation_fails_fast_while_session_synchronization_is_pending
    with_session do |root, path|
      client = FakeClient.new(positions: { nil => { known: true, leaf_id: nil, error: nil } })
      synchronizer = build_synchronizer(root, FakeRegistry.new(client))
      operation_started = Queue.new
      release_operation = Queue.new

      operation = Thread.new do
        synchronizer.with_mutable_client(path) do
          operation_started << true
          release_operation.pop
        end
      end
      operation_started.pop

      error = assert_raises(Sessions::SessionSynchronizer::BusyError) do
        synchronizer.with_mutable_client(path) { flunk "second operation should not start" }
      end
      assert_includes error.message, "pending"
    ensure
      release_operation << true if operation&.alive?
      operation&.join
    end
  end

  def test_bash_releases_the_session_lock_after_verification
    with_session do |root, path|
      client = FakeClient.new(positions: { nil => { known: true, leaf_id: nil, error: nil } })
      registry = PiRpcClientRegistry.new(factory: ->(_path) { raise "unexpected start" })
      registry.register(path, client)
      synchronizer = build_synchronizer(root, registry)
      operation_ran = false

      synchronizer.with_bash_client(path) do
        synchronizer.with_mutable_client(path) { operation_ran = true }
      end

      assert operation_ran
    end
  end

  def test_operation_reconciles_bash_append_during_verification
    with_session do |root, path|
      operation_checked_position = Queue.new
      release_operation_check = Queue.new
      bash_started = Queue.new
      release_bash = Queue.new
      entries = []
      position_checks = 0
      client = Object.new
      client.define_singleton_method(:session_position) do |_cursor|
        position_checks += 1
        if position_checks == 3
          operation_checked_position << true
          release_operation_check.pop
          { known: true, leaf_id: nil, error: nil }
        else
          { known: true, leaf_id: entries.last&.fetch("id", nil), error: nil }
        end
      end
      client.define_singleton_method(:session_entries_after) do |_cursor|
        { known: true, leaf_id: entries.last&.fetch("id", nil), entries: entries.dup, error: nil }
      end
      client.define_singleton_method(:busy?) { false }
      client.define_singleton_method(:close) { nil }
      registry = PiRpcClientRegistry.new(factory: ->(_path) { raise "unexpected start" })
      registry.register(path, client)
      synchronizer = build_synchronizer(root, registry)

      bash = Thread.new do
        synchronizer.with_bash_client(path) do
          bash_started << true
          release_bash.pop
        end
      end
      bash_started.pop
      operation_ran = false
      operation = Thread.new { synchronizer.with_mutable_client(path) { operation_ran = true } }
      operation_checked_position.pop
      append(path, type: "message", id: "bash-1", parentId: nil, message: { role: "bashExecution", command: "pwd", output: "done", exitCode: 0 })
      entries << { "id" => "bash-1" }
      release_operation_check << true
      operation.join

      assert operation_ran
      assert_equal :managed, synchronizer.inspect(path).mode
    ensure
      release_operation_check << true if operation&.alive?
      release_bash << true if bash&.alive?
      operation&.join
      bash&.join
    end
  end

  def test_interrupt_does_not_wait_for_synchronized_operation
    with_session do |root, path|
      client = FakeClient.new(positions: { nil => { known: true, leaf_id: nil, error: nil } })
      synchronizer = build_synchronizer(root, FakeRegistry.new(client))
      operation_started = Queue.new
      release_operation = Queue.new
      interrupted = Queue.new

      operation = Thread.new do
        synchronizer.with_mutable_client(path) do
          operation_started << true
          release_operation.pop
        end
      end
      operation_started.pop

      synchronizer.with_interrupt_client(path) { interrupted << true }

      assert interrupted.pop(timeout: 1)
    ensure
      release_operation << true if operation&.alive?
      operation&.join
    end
  end

  def test_interrupt_fails_if_pi_cli_writes_during_rpc_verification
    with_session do |root, path|
      calls = 0
      client = HookClient.new do
        calls += 1
        append(path, type: "message", id: "external", parentId: nil, message: { role: "user", content: [] }) if calls == 2
      end
      synchronizer = build_synchronizer(root, FakeRegistry.new(client))

      error = assert_raises(Sessions::SessionSynchronizer::BlockedError) do
        synchronizer.with_interrupt_client(path) { flunk "interrupt should not run" }
      end

      assert_equal :external_follow, error.mode
      assert_includes error.message, "Pi CLI"
    end
  end

  def test_takeover_fails_if_pi_cli_writes_during_fresh_rpc_startup
    with_session do |root, path|
      append(path, type: "message", id: "external", parentId: nil, message: { role: "user", content: [] })
      stale = FakeClient.new(positions: { "external" => { known: false, leaf_id: nil, error: nil } })
      fresh = HookClient.new do
        append(path, type: "message", id: "later", parentId: "external", message: { role: "assistant", content: [] })
      end
      registry = FakeRegistry.new(stale, replacement: fresh)
      synchronizer = build_synchronizer(root, registry)
      synchronizer.inspect(path)

      error = assert_raises(Sessions::SessionSynchronizer::BlockedError) do
        synchronizer.take_over(path)
      end

      assert_equal :external_follow, error.mode
      assert_includes error.message, "Pi CLI"
      assert fresh.closed
      assert_equal :external_follow, synchronizer.inspect(path).mode
    end
  end

  def test_unsupported_get_entries_fails_closed
    with_session do |root, path|
      append(path, type: "message", id: "persisted", parentId: nil, message: { role: "user", content: [] })
      client = FakeClient.new(positions: {
        "persisted" => { known: false, leaf_id: nil, error: "Unknown command type: get_entries" }
      })
      synchronizer = build_synchronizer(root, FakeRegistry.new(client))

      result = synchronizer.inspect(path)

      assert_equal :conflict, result.mode
      assert_includes result.error, "get_entries"
    end
  end

  private

  def build_synchronizer(root, registry)
    Sessions::SessionSynchronizer.new(
      sessions_root: root,
      rpc_clients: registry
    )
  end

  def with_session
    Dir.mktmpdir do |root|
      path = File.join(root, "session.jsonl")
      File.write(path, JSON.generate(type: "session", id: "session-1", cwd: "/tmp/project") + "\n")
      yield root, path
    end
  end

  def append(path, **entry)
    File.open(path, "a") { |file| file.puts(JSON.generate(entry)) }
  end
end
