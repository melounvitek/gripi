require "json"
require "securerandom"
require "time"
require_relative "secure_state_file"

class BrowserAccessStore
  PRUNE_INTERVAL = 24 * 60 * 60
  UNREQUESTED_RETENTION = 7 * 24 * 60 * 60
  DENIED_RETENTION = 7 * 24 * 60 * 60
  REQUESTED_RETENTION = 30 * 24 * 60 * 60
  MAX_PENDING_REQUESTS = 100
  MAX_TERMINAL_REQUESTS = 100
  MAX_IP_BYTES = 64
  MAX_USER_AGENT_BYTES = 512

  class PendingRequestsFull < StandardError
    attr_reader :limit

    def initialize(limit)
      @limit = limit
      super("Pending browser request limit reached (#{limit})")
    end
  end

  def initialize(path:)
    @file = SecureStateFile.new(path)
    @mutex = Mutex.new
    @last_pruned_at = Time.at(0)
  end

  def approved?(token)
    return false if token.to_s.empty?

    prune_stale_pending_requests_if_due
    data.fetch("approved_browsers", []).any? { |browser| browser["token"] == token }
  end

  def ensure_pending(token:, ip:, user_agent:)
    now = Time.now.utc.iso8601
    update do |state|
      request = state.fetch("pending_requests").find { |item| item["token"] == token }
      unless request
        enforce_pending_limit!(state)
        request = {
          "code" => unique_code(state),
          "token" => token,
          "requested" => false,
          "created_at" => now,
          "requested_at" => nil,
          "ip" => bounded_string(ip, MAX_IP_BYTES),
          "user_agent" => bounded_string(user_agent, MAX_USER_AGENT_BYTES)
        }
        state.fetch("pending_requests") << request
      end
      request
    end
  end

  def request_access(token, ip: nil, user_agent: nil)
    now = Time.now.utc.iso8601
    update do |state|
      request = state.fetch("pending_requests").find { |item| item["token"] == token }
      unless request
        enforce_pending_limit!(state)
        request = {
          "code" => unique_code(state),
          "token" => token,
          "requested" => false,
          "created_at" => now,
          "requested_at" => nil,
          "ip" => bounded_string(ip, MAX_IP_BYTES),
          "user_agent" => bounded_string(user_agent, MAX_USER_AGENT_BYTES)
        }
        state.fetch("pending_requests") << request
      end
      enforce_pending_limit!(state, excluding: request) if request["denied_at"]
      request.delete("denied_at")
      request["requested"] = true
      request["requested_at"] = now
      request
    end
  end

  def pending?(token)
    data.fetch("pending_requests", []).any? { |request| request["token"] == token && !request["denied_at"] }
  end

  def pending_requests
    data.fetch("pending_requests", []).select { |request| request["requested"] && !request["denied_at"] }
  end

  def approve_code(code)
    approve_request { |request| request["code"] == code }
  end

  def approve_token(token)
    approve_request { |request| request["token"] == token }
  end

  def approve_current_browser(token, label: nil)
    return if token.to_s.empty?

    update do |state|
      add_approved_browser(state, token, label)
      state.fetch("pending_requests").reject! { |request| request["token"] == token }
      true
    end
  end

  def deny_code(code)
    update do |state|
      request = state.fetch("pending_requests").find { |item| item["code"] == code }
      request["denied_at"] = Time.now.utc.iso8601 if request
      request
    end
  end

  def pending_status(token)
    return "approved" if approved?(token)

    request = data.fetch("pending_requests", []).find { |item| item["token"] == token }
    return "denied" if request && request["denied_at"]
    request && request["requested"] ? "pending" : "created"
  end

  private

  def approve_request
    update do |state|
      request = state.fetch("pending_requests").find { |item| yield item }
      if request
        add_approved_browser(state, request.fetch("token"), request["user_agent"])
        state.fetch("pending_requests").delete(request)
      end
      request
    end
  end

  def add_approved_browser(state, token, label)
    return if state.fetch("approved_browsers").any? { |browser| browser["token"] == token }

    state.fetch("approved_browsers") << {
      "token" => token,
      "approved_at" => Time.now.utc.iso8601,
      "label" => bounded_string(label, MAX_USER_AGENT_BYTES)
    }
  end

  def enforce_pending_limit!(state, excluding: nil)
    active_count = state.fetch("pending_requests").count do |request|
      request != excluding && !request["denied_at"]
    end
    return if active_count < MAX_PENDING_REQUESTS

    raise PendingRequestsFull, MAX_PENDING_REQUESTS
  end

  def bounded_string(value, max_bytes)
    value.to_s.byteslice(0, max_bytes).to_s.scrub("")
  end

  def unique_code(state)
    loop do
      code = SecureRandom.alphanumeric(8).upcase.scan(/.{1,4}/).join("-")
      return code unless state.fetch("pending_requests").any? { |request| request["code"] == code }
    end
  end

  def data
    @mutex.synchronize { read_state }
  end

  def update
    @mutex.synchronize do
      state = read_state
      prune_pending_requests!(state)
      result = yield state
      prune_pending_requests!(state)
      prune_terminal_requests!(state)
      write_state(state)
      @last_pruned_at = Time.now
      result
    end
  end

  def prune_stale_pending_requests_if_due
    now = Time.now
    return if now - @last_pruned_at < PRUNE_INTERVAL

    @mutex.synchronize do
      now = Time.now
      return if now - @last_pruned_at < PRUNE_INTERVAL

      state = read_state
      changed = prune_pending_requests!(state, now: now)
      changed = prune_terminal_requests!(state) || changed
      write_state(state) if changed
      @last_pruned_at = now
    end
  end

  def prune_pending_requests!(state, now: Time.now)
    before_count = state.fetch("pending_requests").length
    state.fetch("pending_requests").reject! do |request|
      stale_pending_request?(request, now)
    end
    state.fetch("pending_requests").length != before_count
  end

  def prune_terminal_requests!(state)
    terminal = state.fetch("pending_requests").select { |request| request["denied_at"] }
    overflow = terminal.length - MAX_TERMINAL_REQUESTS
    return false unless overflow.positive?

    remove = terminal.sort_by { |request| parse_time(request["denied_at"]) || Time.at(0) }.first(overflow)
    state.fetch("pending_requests").reject! { |request| remove.include?(request) }
    true
  end

  def stale_pending_request?(request, now)
    if request["denied_at"]
      timestamp_stale?(request["denied_at"], now, DENIED_RETENTION)
    elsif request["requested"]
      timestamp_stale?(request["requested_at"] || request["created_at"], now, REQUESTED_RETENTION)
    else
      timestamp_stale?(request["created_at"], now, UNREQUESTED_RETENTION)
    end
  end

  def timestamp_stale?(timestamp, now, retention)
    time = parse_time(timestamp)
    !time || now - time > retention
  end

  def parse_time(timestamp)
    Time.parse(timestamp.to_s)
  rescue ArgumentError
    nil
  end

  def read_state
    contents = @file.read
    return empty_state unless contents

    parsed = JSON.parse(contents)
    pending_requests = Array(parsed["pending_requests"])
    pending_requests.each do |request|
      request["ip"] = bounded_string(request["ip"], MAX_IP_BYTES)
      request["user_agent"] = bounded_string(request["user_agent"], MAX_USER_AGENT_BYTES)
    end
    {
      "approved_browsers" => Array(parsed["approved_browsers"]),
      "pending_requests" => pending_requests
    }
  rescue JSON::ParserError
    empty_state
  end

  def write_state(state)
    @file.write(JSON.pretty_generate(state) + "\n")
  end

  def empty_state
    { "approved_browsers" => [], "pending_requests" => [] }
  end
end
