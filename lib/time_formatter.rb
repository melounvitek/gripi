require "date"

class TimeFormatter
  def self.relative(time, now: Time.now)
    return "unknown" unless time

    local_time = time.localtime
    local_now = now.localtime
    seconds_ago = (local_now - local_time).to_i
    return "just now" if seconds_ago < 60

    if local_time.to_date == local_now.to_date
      minutes_ago = seconds_ago / 60
      return pluralize(minutes_ago, "minute") if minutes_ago < 60

      hours_ago = seconds_ago / 3600
      return pluralize(hours_ago, "hour")
    end

    return "yesterday" if local_time.to_date == local_now.to_date - 1

    local_time.strftime("%Y-%m-%d")
  end

  def self.pluralize(count, unit)
    "#{count} #{unit}#{"s" unless count == 1} ago"
  end

  private_class_method :pluralize
end
