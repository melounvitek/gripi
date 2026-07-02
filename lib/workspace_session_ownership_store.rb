require "json"
require "fileutils"
require "digest"

class WorkspaceSessionOwnershipStore
  def initialize(path:)
    @path = path
    @mutex = Mutex.new
  end

  def claim(session_path, workspace_id)
    return if session_path.to_s.empty? || workspace_id.to_s.empty?

    update do |state|
      state.fetch("sessions")[canonical_path(session_path)] = workspace_id
    end
  end

  def owned_by?(session_path, workspace_id)
    return false if session_path.to_s.empty? || workspace_id.to_s.empty?

    data.fetch("sessions", {})[canonical_path(session_path)] == workspace_id
  end

  def owns_session_hash?(session_hash, workspace_id)
    return false if session_hash.to_s.empty? || workspace_id.to_s.empty?

    data.fetch("sessions", {}).any? do |session_path, owner|
      owner == workspace_id && Digest::SHA256.hexdigest(session_path) == session_hash
    end
  end

  def filter_sessions(sessions, workspace_id)
    sessions.select { |session| owned_by?(session.path, workspace_id) }
  end

  private

  def canonical_path(session_path)
    File.expand_path(session_path.to_s)
  end

  def data
    @mutex.synchronize { read_state }
  end

  def update
    @mutex.synchronize do
      state = read_state
      yield state
      write_state(state)
    end
  end

  def read_state
    return empty_state unless File.exist?(@path)

    parsed = JSON.parse(File.read(@path))
    { "sessions" => parsed.fetch("sessions", {}) }
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
    { "sessions" => {} }
  end
end
