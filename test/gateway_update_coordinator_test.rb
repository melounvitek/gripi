require "minitest/autorun"
require "thread"
require_relative "../lib/gateway_updater"
require_relative "../lib/gateway_update_coordinator"

class GatewayUpdateCoordinatorTest < Minitest::Test
  FakeUpdater = Struct.new(:status_result, :update_action) do
    attr_reader :status_calls, :update_calls

    def status
      @status_calls = status_calls.to_i + 1
      status_result
    end

    def update
      @update_calls = update_calls.to_i + 1
      update_action.call
    end
  end

  def test_checks_status_synchronously_when_idle
    updater = FakeUpdater.new(status(:available, target_sha: "target", behind_count: 2), -> {})
    coordinator = GatewayUpdateCoordinator.new(updater:, restarter: -> {})

    snapshot = coordinator.status

    assert_equal :available, snapshot.state
    assert_equal "target", snapshot.target_sha
    assert_equal 2, snapshot.behind_count
    assert_equal 1, updater.status_calls
  end

  def test_starts_only_one_background_update_and_serves_progress_without_another_status_check
    release_update = Queue.new
    updater = FakeUpdater.new(status(:available), -> { release_update.pop })
    coordinator = GatewayUpdateCoordinator.new(updater:, restarter: -> {})

    first = coordinator.start
    second = coordinator.start
    progress = coordinator.status
    wait_until { updater.update_calls == 1 }

    assert_equal :updating, first.state
    assert_equal :updating, second.state
    assert_equal :updating, progress.state
    assert_equal 1, updater.update_calls
    assert_equal 0, updater.status_calls.to_i
  ensure
    release_update << true if release_update
  end

  def test_waits_for_busy_sessions_before_updating
    active_session_count = 2
    sleep_started = Queue.new
    release_sleep = Queue.new
    updater = FakeUpdater.new(nil, -> { status(:current) })
    coordinator = GatewayUpdateCoordinator.new(
      updater:,
      restarter: -> {},
      active_session_count: -> { active_session_count },
      sleeper: ->(delay) do
        if delay == 1
          sleep_started << true
          release_sleep.pop
        end
      end
    )

    initial = coordinator.start
    sleep_started.pop

    assert_equal :waiting, initial.state
    assert_equal 2, initial.active_session_count
    assert_equal "Waiting for 2 active Pi sessions to finish…", initial.message
    assert_equal 0, updater.update_calls.to_i

    active_session_count = 0
    release_sleep << true
    wait_until { updater.update_calls == 1 }
  ensure
    release_sleep << true if release_sleep&.empty?
  end

  def test_waits_again_when_work_starts_during_the_update
    active_session_count = 0
    update_started = Queue.new
    release_update = Queue.new
    wait_started = Queue.new
    release_wait = Queue.new
    restarted = Queue.new
    result = GatewayUpdater::UpdateResult.new(
      state: :updated,
      status: status(:available, current_sha: "old", target_sha: "new"),
      message: "Updated to new"
    )
    updater = FakeUpdater.new(nil, lambda {
      update_started << true
      release_update.pop
      result
    })
    coordinator = GatewayUpdateCoordinator.new(
      updater:,
      restarter: -> { restarted << true },
      active_session_count: -> { active_session_count },
      restart_delay: 0,
      sleeper: ->(delay) do
        if delay == 1
          wait_started << true
          release_wait.pop
        end
      end
    )

    coordinator.start
    update_started.pop
    active_session_count = 1
    release_update << true
    wait_started.pop

    snapshot = coordinator.status
    assert_equal :waiting, snapshot.state
    assert_equal 1, snapshot.active_session_count
    assert restarted.empty?

    active_session_count = 0
    release_wait << true
    wait_until { !restarted.empty? }
  ensure
    release_update << true if release_update&.empty?
    release_wait << true if release_wait&.empty?
  end

  def test_waits_when_work_starts_during_the_restart_delay
    active_session_count = 0
    delay_started = Queue.new
    release_delay = Queue.new
    wait_started = Queue.new
    release_wait = Queue.new
    restarted = Queue.new
    result = GatewayUpdater::UpdateResult.new(
      state: :updated,
      status: status(:available, current_sha: "old", target_sha: "new")
    )
    updater = FakeUpdater.new(nil, -> { result })
    coordinator = GatewayUpdateCoordinator.new(
      updater:,
      restarter: -> { restarted << true },
      active_session_count: -> { active_session_count },
      restart_delay: 0.25,
      sleeper: ->(delay) do
        if delay == 0.25
          delay_started << true
          release_delay.pop
        else
          wait_started << true
          release_wait.pop
        end
      end
    )

    coordinator.start
    delay_started.pop
    active_session_count = 1
    release_delay << true
    wait_started.pop

    assert_equal :waiting, coordinator.status.state
    assert restarted.empty?

    active_session_count = 0
    release_wait << true
    wait_until { !restarted.empty? }
  ensure
    release_delay << true if release_delay&.empty?
    release_wait << true if release_wait&.empty?
  end

  def test_exposes_restarting_before_waiting_and_invoking_the_restarter
    sleep_started = Queue.new
    release_sleep = Queue.new
    restarted = Queue.new
    result = GatewayUpdater::UpdateResult.new(
      state: :updated,
      status: status(:available, current_sha: "old", target_sha: "new", behind_count: 1, summary: "new change"),
      message: "Updated to new"
    )
    updater = FakeUpdater.new(nil, -> { result })
    coordinator = GatewayUpdateCoordinator.new(
      updater:,
      restarter: -> { restarted << true },
      restart_delay: 0.25,
      sleeper: ->(delay) { sleep_started << delay; release_sleep.pop }
    )

    coordinator.start
    assert_equal 0.25, sleep_started.pop
    snapshot = coordinator.status

    assert_equal :restarting, snapshot.state
    assert_equal "old", snapshot.current_sha
    assert_equal "new", snapshot.target_sha
    assert_equal "Updated to new", snapshot.message
    assert restarted.empty?

    release_sleep << true
    wait_until { !restarted.empty? }
  ensure
    release_sleep << true if release_sleep&.empty?
  end

  def test_retries_a_failed_restart_without_running_the_update_again
    restart_attempts = 0
    result = GatewayUpdater::UpdateResult.new(
      state: :updated,
      status: status(:available, current_sha: "old", target_sha: "new"),
      message: "Updated to new"
    )
    updater = FakeUpdater.new(nil, -> { result })
    coordinator = GatewayUpdateCoordinator.new(
      updater:,
      restarter: -> { restart_attempts += 1; raise "shutdown failed" if restart_attempts == 1 },
      restart_delay: 0,
      sleeper: ->(_delay) {}
    )

    coordinator.start
    wait_until { coordinator.status.reason == :restart }
    retry_snapshot = coordinator.start
    wait_until { restart_attempts == 2 }

    assert_equal :restarting, retry_snapshot.state
    assert_equal 1, updater.update_calls
    assert_equal 2, restart_attempts
  end

  def test_preserves_restart_context_when_retry_thread_cannot_be_created
    thread_attempts = 0
    result = GatewayUpdater::UpdateResult.new(
      state: :updated,
      status: status(:available, current_sha: "old", target_sha: "new"),
      message: "Updated to new"
    )
    updater = FakeUpdater.new(nil, -> { result })
    coordinator = GatewayUpdateCoordinator.new(
      updater:,
      restarter: -> { raise "shutdown failed" },
      restart_delay: 0,
      sleeper: ->(_delay) {},
      thread_factory: lambda { |&block|
        thread_attempts += 1
        raise "threads unavailable" if thread_attempts == 2

        Thread.new(&block)
      }
    )

    coordinator.start
    wait_until { coordinator.status.reason == :restart }
    snapshot = coordinator.start

    assert_equal :error, snapshot.state
    assert_equal :restart, snapshot.reason
    assert_equal "new", snapshot.target_sha
    assert_equal 1, updater.update_calls
  end

  def test_captures_update_failures_instead_of_leaking_them_from_the_thread
    updater = FakeUpdater.new(nil, -> { raise "bundle exploded" })
    coordinator = GatewayUpdateCoordinator.new(updater:, restarter: -> {})

    coordinator.start
    snapshot = nil
    wait_until do
      candidate = coordinator.status
      snapshot = candidate if candidate.state == :error
    end

    assert_equal :error, snapshot.state
    assert_equal :update, snapshot.reason
    assert_equal "bundle exploded", snapshot.message
  end

  def test_exposes_unsuccessful_update_results_to_every_status_request_and_allows_retry
    result = GatewayUpdater::UpdateResult.new(
      state: :dependency_failed,
      status: status(:available, current_sha: "old", target_sha: "new"),
      rolled_back: true,
      message: "Dependency installation failed"
    )
    updater = FakeUpdater.new(nil, -> { result })
    coordinator = GatewayUpdateCoordinator.new(updater:, restarter: -> {})

    coordinator.start
    wait_until { coordinator.status.state == :dependency_failed }

    assert_equal :dependency_failed, coordinator.status.state
    assert_equal 0, updater.status_calls.to_i

    coordinator.start
    wait_until { updater.update_calls == 2 }

    assert_equal 2, updater.update_calls
  end

  def test_recovers_when_the_background_thread_cannot_be_created
    updater = FakeUpdater.new(nil, -> {})
    coordinator = GatewayUpdateCoordinator.new(
      updater:,
      restarter: -> {},
      thread_factory: ->(&_block) { raise "threads unavailable" }
    )

    snapshot = coordinator.start

    assert_equal :error, snapshot.state
    assert_equal :update, snapshot.reason
    assert_equal "threads unavailable", snapshot.message
    assert_equal :error, coordinator.status.state
    assert_equal 0, updater.update_calls.to_i
  end

  private

  def status(state, **attributes)
    GatewayUpdater::Status.new(state:, **attributes)
  end

  def wait_until(timeout: 1)
    deadline = Process.clock_gettime(Process::CLOCK_MONOTONIC) + timeout
    loop do
      result = block_given? ? yield : nil
      return result if result
      raise "timed out" if Process.clock_gettime(Process::CLOCK_MONOTONIC) >= deadline

      sleep 0.005
    end
  end
end
