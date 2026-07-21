require "thread"

module Rpc
  class IdleClientMaintenance
    def initialize(interval:, cleanup:, on_error: nil)
      raise ArgumentError, "interval must be positive" unless interval.positive?

      @interval = interval
      @cleanup = cleanup
      @on_error = on_error
      @mutex = Mutex.new
      @condition = ConditionVariable.new
      @thread = nil
      @stopping = false
    end

    def start
      @mutex.synchronize do
        return false if @thread&.alive?

        @stopping = false
        @thread = Thread.new { run }
      end
      true
    end

    def stop
      thread = @mutex.synchronize do
        return false unless @thread

        @stopping = true
        @condition.broadcast
        @thread
      end
      thread.join
      @mutex.synchronize { @thread = nil if @thread.equal?(thread) }
      true
    end

    private

    def run
      loop do
        stopping = @mutex.synchronize do
          @condition.wait(@mutex, @interval) unless @stopping
          @stopping
        end
        break if stopping

        @cleanup.call
      rescue StandardError => error
        begin
          @on_error&.call(error)
        rescue StandardError => reporting_error
          warn("Idle client maintenance error reporting failed: #{reporting_error.class}: #{reporting_error.message}")
        end
      end
    end
  end
end
