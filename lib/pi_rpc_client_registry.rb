require "thread"

class PiRpcClientRegistry
  Entry = Struct.new(:client, :last_used_at, :active_requests, :operation_mutex, keyword_init: true)

  def initialize(factory:, clock: -> { Time.now })
    @factory = factory
    @clock = clock
    @clients = {}
    @mutex = Mutex.new
  end

  def ensure_client(session_path)
    @mutex.synchronize do
      entry = @clients[session_path] ||= new_entry(@factory.call(session_path))
      touch_entry(entry)
      entry.client
    end
  end

  def register(session_path, client)
    old_client = nil
    @mutex.synchronize do
      old_client = @clients[session_path]&.client unless @clients[session_path]&.client.equal?(client)
      @clients[session_path] = new_entry(client)
    end
    old_client&.close
  end

  def client_for(session_path)
    @mutex.synchronize { @clients[session_path]&.client }
  end

  def active?(session_path)
    !!client_for(session_path)
  end

  def event_sequence(session_path)
    client = client_for(session_path)
    client&.respond_to?(:event_sequence) ? client.event_sequence : 0
  end

  def live_snapshot(session_path)
    client = client_for(session_path)
    return { event_sequence: 0, active_tool_events: [] } unless client
    return client.live_snapshot if client.respond_to?(:live_snapshot)

    {
      event_sequence: client.respond_to?(:event_sequence) ? client.event_sequence : 0,
      active_tool_events: []
    }
  end

  def busy?(session_path)
    client = client_for(session_path)
    client&.respond_to?(:busy?) ? client.busy? : false
  end

  def busy_since(session_path)
    client = client_for(session_path)
    client&.respond_to?(:busy_since) ? client.busy_since : nil
  end

  def compacting?(session_path)
    client = client_for(session_path)
    client&.respond_to?(:compacting?) ? client.compacting? : false
  end

  def agent_running?(session_path)
    client = client_for(session_path)
    client&.respond_to?(:agent_running?) ? client.agent_running? : false
  end

  def begin_use(session_path)
    @mutex.synchronize do
      entry = @clients[session_path]
      return unless entry

      entry.active_requests += 1
      touch_entry(entry)
      entry.client
    end
  end

  def end_use(session_path)
    @mutex.synchronize do
      entry = @clients[session_path]
      return unless entry

      entry.active_requests -= 1 if entry.active_requests.positive?
      touch_entry(entry)
    end
  end

  def with_client(session_path)
    entry = @mutex.synchronize do
      existing_entry = @clients[session_path] ||= new_entry(@factory.call(session_path))
      existing_entry.active_requests += 1
      touch_entry(existing_entry)
      existing_entry
    end

    entry.operation_mutex.synchronize { yield entry.client }
  ensure
    end_use(session_path)
  end

  def move(old_path, new_path)
    old_client = nil
    @mutex.synchronize do
      entry = @clients.delete(old_path)
      return unless entry

      old_client = @clients[new_path]&.client unless @clients[new_path]&.client.equal?(entry.client)
      touch_entry(entry)
      @clients[new_path] = entry
    end
    old_client&.close
  end

  def events_after(session_path, after_seq)
    client = begin_use(session_path)
    return { events: [], last_seq: 0, missed: false } unless client

    client.events_after(after_seq)
  ensure
    end_use(session_path) if client
  end

  def close_idle_clients(idle_timeout:, now: @clock.call, except: [])
    clients_to_close = []
    closed_paths = []

    @mutex.synchronize do
      @clients.delete_if do |session_path, entry|
        idle = now - entry.last_used_at >= idle_timeout
        close = idle && entry.active_requests.zero? && !except.include?(session_path)
        if close
          clients_to_close << entry.client
          closed_paths << session_path
        end
        close
      end
    end

    clients_to_close.each(&:close)
    closed_paths
  end

  def close_all
    clients = @mutex.synchronize do
      existing = @clients.values.map(&:client)
      @clients = {}
      existing
    end
    clients.each(&:close)
  end

  private

  def new_entry(client)
    Entry.new(client: client, last_used_at: @clock.call, active_requests: 0, operation_mutex: Mutex.new)
  end

  def touch_entry(entry)
    entry.last_used_at = @clock.call
  end
end
