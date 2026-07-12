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
    keyword_init: true
  )

  def initialize(updater:, restarter:, restart_delay: 1, sleeper: ->(delay) { sleep(delay) }, thread_factory: ->(&block) { Thread.new(&block) })
    @updater = updater
    @restarter = restarter
    @restart_delay = restart_delay
    @sleeper = sleeper
    @thread_factory = thread_factory
    @mutex = Mutex.new
    @running = false
    @finished = false
    @restart_pending = false
  end

  def status
    @mutex.synchronize do
      return @snapshot if @running || @finished

      @snapshot = snapshot_from_status(@updater.status)
    rescue StandardError => error
      @snapshot = failure_snapshot(error, :status)
    end
  end

  def start
    @mutex.synchronize do
      return @snapshot if @running

      @running = true
      @finished = false
      operation = @restart_pending ? :restart : :update
      action = operation == :restart ? method(:perform_restart) : method(:perform_update)
      @snapshot = operation == :restart ? restarting_snapshot : progress_snapshot
      begin
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
    result = @updater.update
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
    @sleeper.call(@restart_delay)
    @restarter.call
  rescue StandardError => error
    finish_with(failure_snapshot(error, :restart, @snapshot))
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
      message: "Restarting gateway…",
      current_sha: previous&.current_sha,
      target_sha: previous&.target_sha,
      behind_count: previous&.behind_count,
      summary: previous&.summary
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
