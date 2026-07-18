require "json"
require "securerandom"
require "time"
require_relative "secure_state_file"

class WorkspaceAccessStore
  PENDING_RETENTION = 30 * 24 * 60 * 60
  MAX_PENDING_REQUESTS = 100
  MAX_TERMINAL_REQUESTS = 100

  class PendingRequestsFull < StandardError
    attr_reader :limit

    def initialize(limit)
      @limit = limit
      super("Pending workspace request limit reached (#{limit})")
    end
  end

  def initialize(path:)
    @file = SecureStateFile.new(path)
    @mutex = Mutex.new
  end

  def approved?(workspace_id)
    return false if workspace_id.to_s.empty?

    data.fetch("approved_workspaces", []).any? { |workspace| workspace["workspace_id"] == workspace_id }
  end

  def request_for_code(code)
    return if code.to_s.empty?

    data.fetch("pending_requests", []).find { |request| request["code"] == code }
  end

  def any_approved?
    !data.fetch("approved_workspaces", []).empty?
  end

  def approve_workspace(workspace_id)
    return if workspace_id.to_s.empty?

    update do |state|
      add_approved_workspace(state, workspace_id)
      state.fetch("pending_requests").reject! { |request| request["workspace_id"] == workspace_id }
      true
    end
  end

  def request_access(workspace_id, browser_token: nil)
    return if workspace_id.to_s.empty?

    now = Time.now.utc.iso8601
    update do |state|
      request = state.fetch("pending_requests").find do |item|
        item["workspace_id"] == workspace_id && item["browser_token"] == browser_token.to_s
      end
      unless request
        enforce_pending_limit!(state)
        request = {
          "code" => unique_code(state),
          "workspace_id" => workspace_id,
          "browser_token" => browser_token.to_s,
          "created_at" => now,
          "requested_at" => now
        }
        state.fetch("pending_requests") << request
      end
      enforce_pending_limit!(state, excluding: request) if request["denied_at"] || request["approved_at"]
      request.delete("denied_at")
      request.delete("approved_at")
      request["requested_at"] = now
      request
    end
  end

  def pending_requests
    data.fetch("pending_requests", []).select { |request| !request["denied_at"] && !request["approved_at"] }
  end

  def approve_code(code)
    update do |state|
      request = state.fetch("pending_requests").find { |item| item["code"] == code }
      if request
        add_approved_workspace(state, request.fetch("workspace_id"))
        request["approved_at"] = Time.now.utc.iso8601
      end
      request
    end
  end

  def deny_code(code)
    update do |state|
      request = state.fetch("pending_requests").find { |item| item["code"] == code }
      request["denied_at"] = Time.now.utc.iso8601 if request
      request
    end
  end

  def pending_status(workspace_id)
    return "approved" if approved?(workspace_id)

    request = data.fetch("pending_requests", []).find { |item| item["workspace_id"] == workspace_id }
    return "approved" if request && request["approved_at"]
    return "denied" if request && request["denied_at"]
    request ? "pending" : "unknown"
  end

  private

  def add_approved_workspace(state, workspace_id)
    return if state.fetch("approved_workspaces").any? { |workspace| workspace["workspace_id"] == workspace_id }

    state.fetch("approved_workspaces") << {
      "workspace_id" => workspace_id,
      "approved_at" => Time.now.utc.iso8601
    }
  end

  def enforce_pending_limit!(state, excluding: nil)
    active_count = state.fetch("pending_requests").count do |request|
      request != excluding && !request["denied_at"] && !request["approved_at"]
    end
    return if active_count < MAX_PENDING_REQUESTS

    raise PendingRequestsFull, MAX_PENDING_REQUESTS
  end

  def unique_code(state)
    loop do
      code = SecureRandom.alphanumeric(8).upcase.scan(/.{1,4}/).join("-")
      return code unless state.fetch("pending_requests").any? { |request| request["code"] == code }
    end
  end

  def data
    @mutex.synchronize do
      state = read_state
      changed = prune_pending_requests!(state)
      changed = prune_terminal_requests!(state) || changed
      write_state(state) if changed
      state
    end
  end

  def update
    @mutex.synchronize do
      state = read_state
      prune_pending_requests!(state)
      result = yield state
      prune_pending_requests!(state)
      prune_terminal_requests!(state)
      write_state(state)
      result
    end
  end

  def prune_pending_requests!(state, now: Time.now)
    before_count = state.fetch("pending_requests").length
    state.fetch("pending_requests").reject! do |request|
      timestamp = request["approved_at"] || request["denied_at"] || request["requested_at"] || request["created_at"]
      stale_timestamp?(timestamp, now)
    end
    state.fetch("pending_requests").length != before_count
  end

  def prune_terminal_requests!(state)
    terminal = state.fetch("pending_requests").select { |request| request["denied_at"] || request["approved_at"] }
    overflow = terminal.length - MAX_TERMINAL_REQUESTS
    return false unless overflow.positive?

    remove = terminal.sort_by do |request|
      Time.parse((request["approved_at"] || request["denied_at"]).to_s)
    rescue ArgumentError
      Time.at(0)
    end.first(overflow)
    state.fetch("pending_requests").reject! { |request| remove.include?(request) }
    true
  end

  def stale_timestamp?(timestamp, now)
    now - Time.parse(timestamp.to_s) > PENDING_RETENTION
  rescue ArgumentError
    true
  end

  def read_state
    contents = @file.read
    return empty_state unless contents

    parsed = JSON.parse(contents)
    {
      "approved_workspaces" => Array(parsed["approved_workspaces"]),
      "pending_requests" => Array(parsed["pending_requests"])
    }
  rescue JSON::ParserError
    empty_state
  end

  def write_state(state)
    @file.write(JSON.pretty_generate(state) + "\n")
  end

  def empty_state
    { "approved_workspaces" => [], "pending_requests" => [] }
  end
end
