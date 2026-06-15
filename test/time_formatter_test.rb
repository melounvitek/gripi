require "minitest/autorun"
require "time"
require_relative "../lib/time_formatter"

class TimeFormatterTest < Minitest::Test
  def test_relative_formats_same_day_times_in_words
    now = Time.parse("2026-06-15 23:30:00 +0200")

    assert_equal "just now", TimeFormatter.relative(now - 30, now: now)
    assert_equal "1 minute ago", TimeFormatter.relative(now - 60, now: now)
    assert_equal "15 minutes ago", TimeFormatter.relative(now - 15 * 60, now: now)
    assert_equal "1 hour ago", TimeFormatter.relative(now - 60 * 60, now: now)
    assert_equal "23 hours ago", TimeFormatter.relative(Time.parse("2026-06-15 00:30:00 +0200"), now: now)
  end

  def test_relative_uses_day_granularity_for_older_times
    now = Time.parse("2026-06-15 00:30:00 +0200")

    assert_equal "yesterday", TimeFormatter.relative(Time.parse("2026-06-14 23:30:00 +0200"), now: now)
    assert_equal "2026-06-13", TimeFormatter.relative(Time.parse("2026-06-13 23:59:00 +0200"), now: now)
  end

  def test_relative_handles_missing_or_future_times
    now = Time.parse("2026-06-15 20:00:00 +0200")

    assert_equal "unknown", TimeFormatter.relative(nil, now: now)
    assert_equal "just now", TimeFormatter.relative(now + 60, now: now)
  end
end
