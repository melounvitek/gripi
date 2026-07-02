require "json"
require "fileutils"
require "securerandom"
require "time"

class WorkspaceAccessStore
  def initialize(path:)
    @path = path
    @mutex = Mutex.new
  end

  def approved?(workspace_id)
    return false if workspace_id.to_s.empty?

    data.fetch("approved_workspaces", []).any? { |workspace| workspace["workspace_id"] == workspace_id }
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
      request = state.fetch("pending_requests").find { |item| item["workspace_id"] == workspace_id }
      unless request
        request = {
          "code" => unique_code(state),
          "workspace_id" => workspace_id,
          "browser_token" => browser_token.to_s,
          "created_at" => now,
          "requested_at" => now
        }
        state.fetch("pending_requests") << request
      end
      request.delete("denied_at")
      request["requested_at"] ||= now
      request
    end
  end

  def pending_requests
    data.fetch("pending_requests", []).select { |request| !request["denied_at"] }
  end

  def approve_code(code)
    update do |state|
      request = state.fetch("pending_requests").find { |item| item["code"] == code }
      if request
        add_approved_workspace(state, request.fetch("workspace_id"))
        state.fetch("pending_requests").delete(request)
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
      result = yield state
      write_state(state)
      result
    end
  end

  def read_state
    return empty_state unless File.exist?(@path)

    parsed = JSON.parse(File.read(@path))
    {
      "approved_workspaces" => Array(parsed["approved_workspaces"]),
      "pending_requests" => Array(parsed["pending_requests"])
    }
  rescue JSON::ParserError
    empty_state
  end

  def write_state(state)
    FileUtils.mkdir_p(File.dirname(@path))
    temp_path = "#{@path}.tmp"
    File.write(temp_path, JSON.pretty_generate(state) + "\n")
    File.rename(temp_path, @path)
  end

  def empty_state
    { "approved_workspaces" => [], "pending_requests" => [] }
  end
end
