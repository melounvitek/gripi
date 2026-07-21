require "json"
require "fileutils"
require "thread"

class GatewayReadStateStore
  @mutexes = {}
  @mutexes_mutex = Mutex.new

  class << self
    def mutex_for(path)
      @mutexes_mutex.synchronize do
        @mutexes[path] ||= Mutex.new
      end
    end
  end

  def initialize(path:)
    @path = path
    @mutex = self.class.mutex_for(path)
  end

  def observe_sessions(sessions)
    @mutex.synchronize do
      state = read_state
      changed = false
      sessions.each do |session|
        response_count = session.assistant_response_count.to_i
        next if state.key?(session.path) && state[session.path] <= response_count

        state[session.path] = response_count
        changed = true
      end
      write_state(state) if changed
    end
  end

  def mark_read(session)
    @mutex.synchronize do
      state = read_state
      state[session.path] = session.assistant_response_count.to_i
      write_state(state)
    end
  end

  def mark_read_count(path, response_count)
    @mutex.synchronize do
      state = read_state
      count = [state.fetch(path, 0), response_count].max
      return if state[path] == count

      state[path] = count
      write_state(state)
    end
  end

  def unread?(session)
    @mutex.synchronize do
      read_state.fetch(session.path, session.assistant_response_count.to_i) < session.assistant_response_count.to_i
    end
  end

  def unread_paths(sessions)
    @mutex.synchronize do
      state = read_state
      sessions.filter_map do |session|
        session.path if state.fetch(session.path, session.assistant_response_count.to_i) < session.assistant_response_count.to_i
      end
    end
  end

  private

  def read_state
    return {} unless File.exist?(@path)

    parsed = JSON.parse(File.read(@path))
    parsed.is_a?(Hash) ? parsed.transform_values(&:to_i) : {}
  rescue JSON::ParserError, SystemCallError
    {}
  end

  def write_state(state)
    FileUtils.mkdir_p(File.dirname(@path))
    temp_path = "#{@path}.tmp-#{$$}-#{Thread.current.object_id}"
    File.write(temp_path, JSON.pretty_generate(state))
    File.rename(temp_path, @path)
  ensure
    File.delete(temp_path) if temp_path && File.exist?(temp_path)
  end
end
