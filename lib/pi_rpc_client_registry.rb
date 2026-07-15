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

  def touch(session_path)
    @mutex.synchronize do
      entry = @clients[session_path]
      touch_entry(entry) if entry
      !!entry
    end
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

    snapshot = {
      event_sequence: client.respond_to?(:event_sequence) ? client.event_sequence : 0,
      active_tool_events: []
    }
    snapshot[:busy] = true if client.respond_to?(:busy?) && client.busy?
    busy_since = client.busy_since if client.respond_to?(:busy_since)
    snapshot[:busy_since] = busy_since if busy_since
    snapshot[:agent_running] = true if client.respond_to?(:agent_running?) && client.agent_running?
    snapshot[:compacting] = true if client.respond_to?(:compacting?) && client.compacting?
    snapshot
  end

  def busy?(session_path)
    client = client_for(session_path)
    client&.respond_to?(:busy?) ? client.busy? : false
  end

  def busy_session_count
    @mutex.synchronize do
      @clients.count { |_session_path, entry| entry.client.respond_to?(:busy?) && entry.client.busy? }
    end
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

  def with_existing_client(session_path, touch: true, &block)
    with_entry(session_path, serialize: true, create: false, touch: touch, &block)
  end

  def with_client(session_path, &block)
    with_entry(session_path, serialize: true, &block)
  end

  def with_interrupt_client(session_path, &block)
    with_entry(session_path, serialize: false, &block)
  end

  def with_existing_interrupt_client(session_path, &block)
    with_entry(session_path, serialize: false, create: false, &block)
  end

  def with_active_client(session_path, touch: true, &block)
    with_entry(session_path, serialize: false, create: false, touch: touch, &block)
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
    with_active_client(session_path, touch: false) { |client| client.events_after(after_seq) } || { events: [], last_seq: 0, missed: false }
  end

  def close_client_if_idle(session_path)
    client = @mutex.synchronize do
      entry = @clients[session_path]
      next unless entry
      next if entry.active_requests.positive?
      next if entry.client.respond_to?(:busy?) && entry.client.busy?

      @clients.delete(session_path)
      entry.client
    end
    return false unless client

    client.close
    true
  end

  def close_idle_clients(idle_timeout:, now: @clock.call, except: [], on_close: nil)
    candidates = @mutex.synchronize do
      @clients.filter_map do |session_path, entry|
        idle = now - entry_activity_at(entry) >= idle_timeout
        busy = entry.client.respond_to?(:busy?) && entry.client.busy?
        session_path if idle && !busy && entry.active_requests.zero? && !except.include?(session_path)
      end
    end
    candidates.each { |session_path| yield session_path } if block_given?

    clients_to_close = []
    closed_paths = []
    @mutex.synchronize do
      @clients.delete_if do |session_path, entry|
        idle = now - entry_activity_at(entry) >= idle_timeout
        busy = entry.client.respond_to?(:busy?) && entry.client.busy?
        close = idle && !busy && entry.active_requests.zero? && !except.include?(session_path)
        if close
          clients_to_close << entry.client
          closed_paths << session_path
          on_close&.call(session_path)
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

  def with_entry(session_path, serialize:, create: true, touch: true)
    entry = acquire_entry(session_path, create: create, touch: touch)
    return unless entry

    if serialize
      entry.operation_mutex.synchronize { yield entry.client }
    else
      yield entry.client
    end
  rescue Errno::EPIPE, IOError
    discard_entry(entry) if entry
    raise
  ensure
    release_entry(entry, touch: touch) if entry
  end

  def acquire_entry(session_path, create:, touch:)
    @mutex.synchronize do
      entry = @clients[session_path]
      entry ||= @clients[session_path] = new_entry(@factory.call(session_path)) if create
      return unless entry

      entry.active_requests += 1
      touch_entry(entry) if touch
      entry
    end
  end

  def release_entry(entry, touch:)
    @mutex.synchronize do
      entry.active_requests -= 1 if entry.active_requests.positive?
      touch_entry(entry) if touch
    end
  end

  def discard_entry(entry)
    client = @mutex.synchronize do
      session_path, current_entry = @clients.find { |_path, candidate| candidate.equal?(entry) }
      @clients.delete(session_path)&.client if current_entry
    end
    client.close if client&.respond_to?(:close)
  end

  def new_entry(client)
    Entry.new(client: client, last_used_at: @clock.call, active_requests: 0, operation_mutex: Mutex.new)
  end

  def touch_entry(entry)
    entry.last_used_at = @clock.call
  end

  def entry_activity_at(entry)
    settled_at = entry.client.settled_at if entry.client.respond_to?(:settled_at)
    [entry.last_used_at, settled_at].compact.max
  end
end
