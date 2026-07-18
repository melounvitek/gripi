class RequestRateLimiter
  DEFAULT_MAX_KEYS = 10_000

  def initialize(limit:, window:, max_keys: DEFAULT_MAX_KEYS, clock: Process.method(:clock_gettime))
    @limit = limit
    @window = window
    @max_keys = max_keys
    @clock = clock
    @attempts = {}
    @last_cleanup_at = 0
    @mutex = Mutex.new
  end

  def allow?(key)
    now = @clock.call(Process::CLOCK_MONOTONIC)
    key = key.to_s
    @mutex.synchronize do
      cleanup_expired_keys(now) if now - @last_cleanup_at >= @window
      return false unless @attempts.key?(key) || @attempts.length < @max_keys

      attempts = (@attempts[key] ||= [])
      attempts.reject! { |timestamp| now - timestamp >= @window }
      return false if attempts.length >= @limit

      attempts << now
      true
    end
  end

  private

  def cleanup_expired_keys(now)
    @attempts.delete_if do |_key, attempts|
      attempts.reject! { |timestamp| now - timestamp >= @window }
      attempts.empty?
    end
    @last_cleanup_at = now
  end
end
