require "minitest/autorun"
require_relative "../lib/request_rate_limiter"

class RequestRateLimiterTest < Minitest::Test
  def test_expires_attempts_and_releases_inactive_keys
    now = 0.0
    limiter = RequestRateLimiter.new(limit: 1, window: 10, max_keys: 2, clock: ->(_clock) { now })

    assert limiter.allow?("one")
    assert limiter.allow?("two")
    refute limiter.allow?("three")

    now = 10.0

    assert limiter.allow?("three")
  end

  def test_limits_repeated_attempts_for_one_key
    limiter = RequestRateLimiter.new(limit: 2, window: 60)

    assert limiter.allow?("client")
    assert limiter.allow?("client")
    refute limiter.allow?("client")
  end
end
