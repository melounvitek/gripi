require "thread"

class GatewayUpdateCoordinator
  Snapshot = Struct.new(
    :state,
    :reason,
    :message,
    :current_sha,
    :target_sha,
    :behind_count,
    :summary,
    :active_session_count,
    keyword_init: true
  )

  def initialize(updater:, restarter:, active_session_count: -> { 0 }, restart_delay: 1, wait_interval: 1, sleeper: ->(delay) { sleep(delay) }, thread_factory: ->(&block) { Thread.new(&block) })
    @updater = updater
    @restarter = restarter
    @active_session_count = active_session_count
    @restart_delay = restart_delay
    @wait_interval = wait_interval
    @sleeper = sleeper
    @thread_factory = thread_factory
    @mutex = Mutex.new
    @operation_mutex = Mutex.new
    @status_checking = false
    @running = false
    @finished = false
    @restart_pending = false
  end

  def cached_status
    @mutex.synchronize do
      @snapshot || Snapshot.new(state: :unknown)
    end
  end

  def status
    @mutex.synchronize do
      return @snapshot if @running || @finished
      return @snapshot || Snapshot.new(state: :unknown) if @status_checking

      @status_checking = true
    end

    begin
      snapshot = @operation_mutex.synchronize { snapshot_from_status(@updater.status) }
    rescue StandardError => error
      snapshot = failure_snapshot(error, :status)
    ensure
      @mutex.synchronize do
        @status_checking = false
        @snapshot = snapshot unless @running || @finished
        snapshot = @snapshot
      end
    end

    snapshot
  end

  def start
    @mutex.synchronize do
      return @snapshot if @running

      @running = true
      @finished = false
      operation = @restart_pending ? :restart : :update
      action = operation == :restart ? method(:perform_restart) : method(:perform_update)
      begin
        active_session_count = @active_session_count.call
        @snapshot = if active_session_count.positive?
          waiting_snapshot(active_session_count)
        elsif operation == :restart
          restarting_snapshot
        else
          progress_snapshot
        end
        @thread_factory.call { action.call }
      rescue StandardError => error
        @running = false
        @finished = true
        @snapshot = failure_snapshot(error, operation, @snapshot)
      end
      @snapshot
    end
  end

  private

  def perform_update
    wait_for_idle
    @mutex.synchronize { @snapshot = progress_snapshot }
    result = @operation_mutex.synchronize { @updater.update }
    unless result&.state == :updated
      finish_with(snapshot_from_result(result))
      return
    end

    @mutex.synchronize do
      status = result.status
      @restart_pending = true
      @snapshot = Snapshot.new(
        state: :restarting,
        message: result.message || "Restarting gateway…",
        current_sha: status&.current_sha,
        target_sha: status&.target_sha,
        behind_count: status&.behind_count,
        summary: status&.summary
      )
    end

    perform_restart
  rescue StandardError => error
    finish_with(failure_snapshot(error, :update, @snapshot))
  end

  def perform_restart
    wait_for_idle
    @mutex.synchronize { @snapshot = restarting_snapshot }
    @sleeper.call(@restart_delay)
    wait_for_idle
    @mutex.synchronize { @snapshot = restarting_snapshot }
    @restarter.call
  rescue StandardError => error
    finish_with(failure_snapshot(error, :restart, @snapshot))
  end

  def wait_for_idle
    loop do
      active_session_count = @active_session_count.call
      return unless active_session_count.positive?

      @mutex.synchronize { @snapshot = waiting_snapshot(active_session_count) }
      @sleeper.call(@wait_interval)
    end
  end

  def finish_with(snapshot)
    @mutex.synchronize do
      @snapshot = snapshot
      @running = false
      @finished = true
    end
  end

  def progress_snapshot
    previous = @snapshot
    Snapshot.new(
      state: :updating,
      message: "Updating gateway…",
      current_sha: previous&.current_sha,
      target_sha: previous&.target_sha,
      behind_count: previous&.behind_count,
      summary: previous&.summary
    )
  end

  def restarting_snapshot
    previous = @snapshot
    Snapshot.new(
      state: :restarting,
      message: previous&.state == :restarting ? previous.message : "Restarting gateway…",
      current_sha: previous&.current_sha,
      target_sha: previous&.target_sha,
      behind_count: previous&.behind_count,
      summary: previous&.summary
    )
  end

  def waiting_snapshot(active_session_count)
    previous = @snapshot
    sessions = active_session_count == 1 ? "session" : "sessions"
    Snapshot.new(
      state: :waiting,
      message: "Waiting for #{active_session_count} active Pi #{sessions} to finish…",
      current_sha: previous&.current_sha,
      target_sha: previous&.target_sha,
      behind_count: previous&.behind_count,
      summary: previous&.summary,
      active_session_count: active_session_count
    )
  end

  def snapshot_from_result(result)
    return failure_snapshot(StandardError.new("The gateway update did not return a result"), :update) unless result

    status = result.status
    Snapshot.new(
      state: result.state,
      reason: status&.reason || result.state,
      message: result.message,
      current_sha: status&.current_sha,
      target_sha: status&.target_sha,
      behind_count: status&.behind_count,
      summary: status&.summary
    )
  end

  def snapshot_from_status(status)
    Snapshot.new(
      state: status.state,
      reason: status.reason,
      message: status.message,
      current_sha: status.current_sha,
      target_sha: status.target_sha,
      behind_count: status.behind_count,
      summary: status.summary
    )
  end

  def failure_snapshot(error, reason, previous = nil)
    Snapshot.new(
      state: :error,
      reason:,
      message: error.message,
      current_sha: previous&.current_sha,
      target_sha: previous&.target_sha,
      behind_count: previous&.behind_count,
      summary: previous&.summary
    )
  end
end
