require "minitest/autorun"
require "timeout"
require_relative "../lib/rpc/idle_client_maintenance"

class RpcIdleClientMaintenanceTest < Minitest::Test
  def test_runs_periodically_and_starts_only_once
    sweeps = Queue.new
    maintenance = Rpc::IdleClientMaintenance.new(interval: 0.01, cleanup: -> { sweeps << true })

    assert maintenance.start
    refute maintenance.start
    2.times { Timeout.timeout(1) { sweeps.pop } }

    assert maintenance.stop
    sweep_count = sweeps.size
    sleep 0.03
    assert_equal sweep_count, sweeps.size
  ensure
    maintenance&.stop
  end

  def test_stops_without_waiting_for_the_interval
    maintenance = Rpc::IdleClientMaintenance.new(interval: 60, cleanup: -> {})
    maintenance.start

    started_at = Process.clock_gettime(Process::CLOCK_MONOTONIC)
    assert maintenance.stop

    assert_operator Process.clock_gettime(Process::CLOCK_MONOTONIC) - started_at, :<, 0.2
  end

  def test_continues_after_a_cleanup_failure
    calls = 0
    errors = Queue.new
    completed = Queue.new
    maintenance = Rpc::IdleClientMaintenance.new(
      interval: 0.01,
      cleanup: -> {
        calls += 1
        raise "failed sweep" if calls == 1

        completed << true
      },
      on_error: ->(error) { errors << error.message }
    )

    maintenance.start

    assert_equal "failed sweep", Timeout.timeout(1) { errors.pop }
    assert Timeout.timeout(1) { completed.pop }
  ensure
    maintenance&.stop
  end

  def test_continues_when_error_reporting_fails
    calls = 0
    completed = Queue.new
    maintenance = Rpc::IdleClientMaintenance.new(
      interval: 0.01,
      cleanup: -> {
        calls += 1
        raise "failed sweep" if calls == 1

        completed << true
      },
      on_error: ->(_error) { raise "failed reporter" }
    )

    _stdout, stderr = capture_io do
      maintenance.start
      assert Timeout.timeout(1) { completed.pop }
      maintenance.stop
    end

    assert_includes stderr, "failed reporter"
  ensure
    maintenance&.stop
  end

  def test_rejects_a_non_positive_interval
    assert_raises(ArgumentError) { Rpc::IdleClientMaintenance.new(interval: 0, cleanup: -> {}) }
  end
end
