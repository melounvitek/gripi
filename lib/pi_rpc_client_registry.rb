require "thread"
require_relative "rpc/diagnostics"

class PiRpcClientRegistry
  class OperationPending < StandardError; end
  class InterruptPending < StandardError; end
  class ClientRetiring < StandardError; end
  class ClientStarting < StandardError; end

  Entry = Struct.new(:client, :last_used_at, :active_requests, :operation_mutex, :interrupt_mutex, :retiring, keyword_init: true)

  def initialize(factory:, clock: -> { Time.now })
    @factory = factory
    @clock = clock
    @clients = {}
    @creating = {}
    @mutex = Mutex.new
  end

  def ensure_client(session_path)
    entry = acquire_entry(session_path, create: true, touch: true)
    entry.client
  ensure
    release_entry(entry, touch: true) if entry
  end

  def register(session_path, client)
    old_client = nil
    @mutex.synchronize do
      current = @clients[session_path]
      raise ClientRetiring, "Pi RPC client is restarting" if current&.retiring
      if current && !current.client.equal?(client) && current.active_requests.positive?
        raise OperationPending, "Session operation is pending"
      end
      next if current&.client.equal?(client)

      old_client = current&.client
      @clients[session_path] = new_entry(client)
    end
    old_client&.close
  end

  def client_for(session_path)
    @mutex.synchronize do
      entry = @clients[session_path]
      entry.client if entry && !entry.retiring
    end
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

  def event_replay_cursor(session_path)
    client = client_for(session_path)
    client&.respond_to?(:event_replay_cursor) ? client.event_replay_cursor : 0
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
    with_entry(session_path, serialize: :operation, create: false, touch: touch, &block)
  end

  def with_client(session_path, &block)
    with_entry(session_path, serialize: :operation, &block)
  end

  def with_interrupt_client(session_path, &block)
    with_entry(session_path, serialize: :interrupt, &block)
  end

  def with_existing_interrupt_client(session_path, &block)
    with_entry(session_path, serialize: :interrupt, create: false, &block)
  end

  def with_active_client(session_path, touch: true, &block)
    with_entry(session_path, serialize: false, create: false, touch: touch, &block)
  end

  def move(old_path, new_path)
    old_client = nil
    @mutex.synchronize do
      raise ClientStarting, "Pi RPC client is starting" if @creating[old_path] || @creating[new_path]

      entry = @clients[old_path]
      return unless entry
      raise ClientRetiring, "Pi RPC client is restarting" if entry.retiring
      raise OperationPending, "Source session operation is pending" if entry.active_requests.positive?

      destination = @clients[new_path]
      raise ClientRetiring, "Pi RPC client is restarting" if destination&.retiring
      if destination && !destination.equal?(entry) && destination.active_requests.positive?
        raise OperationPending, "Destination session operation is pending"
      end

      @clients.delete(old_path)
      old_client = destination&.client unless destination&.client.equal?(entry.client)
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
      @creating.clear
      existing
    end
    clients.each(&:close)
  end

  private

  def with_entry(session_path, serialize:, create: true, touch: true)
    entry = acquire_entry(session_path, create: create, touch: touch)
    return unless entry

    lock = case serialize
    when :operation then entry.operation_mutex
    when :interrupt then entry.interrupt_mutex
    end
    unless lock
      return yield entry.client
    end

    error_class = serialize == :interrupt ? InterruptPending : OperationPending
    unless lock.try_lock
      Rpc::Diagnostics.log("operation_rejected", session: session_path, lane: serialize)
      raise error_class, "Another #{serialize} is already pending for this session"
    end

    begin
      yield entry.client
    ensure
      lock.unlock
    end
  rescue Errno::EPIPE, IOError => error
    discard_entry(entry, reason: error.class.name) if entry
    raise
  ensure
    release_entry(entry, touch: touch) if entry
  end

  def acquire_entry(session_path, create:, touch:)
    creation_token = nil
    entry = @mutex.synchronize do
      existing = @clients[session_path]
      raise ClientRetiring, "Pi RPC client is restarting" if existing&.retiring
      if existing
        existing.active_requests += 1
        touch_entry(existing) if touch
        next existing
      end
      next unless create
      raise ClientStarting, "Pi RPC client is starting" if @creating[session_path]

      creation_token = Object.new
      @creating[session_path] = creation_token
      nil
    end
    return entry if entry
    return unless create

    begin
      client = @factory.call(session_path)
    rescue StandardError
      @mutex.synchronize { @creating.delete(session_path) if @creating[session_path].equal?(creation_token) }
      raise
    end
    install_created_entry(session_path, client, creation_token, touch: touch)
  end

  def install_created_entry(session_path, client, creation_token, touch:)
    unused_client = nil
    error = nil
    entry = @mutex.synchronize do
      unless @creating[session_path].equal?(creation_token)
        unused_client = client
        error = ClientStarting.new("Pi RPC client creation was cancelled")
        next
      end

      @creating.delete(session_path)
      existing = @clients[session_path]
      if existing&.retiring
        unused_client = client
        error = ClientRetiring.new("Pi RPC client is restarting")
        next
      end

      if existing
        unused_client = client
        client_entry = existing
      else
        client_entry = new_entry(client)
        @clients[session_path] = client_entry
      end
      client_entry.active_requests += 1
      touch_entry(client_entry) if touch
      client_entry
    end
    unused_client&.close
    raise error if error

    entry
  end

  def release_entry(entry, touch:)
    @mutex.synchronize do
      entry.active_requests -= 1 if entry.active_requests.positive?
      touch_entry(entry) if touch
    end
  end

  def discard_entry(entry, reason:)
    session_path = nil
    owns_retirement = false
    client = @mutex.synchronize do
      session_path, current_entry = @clients.find { |_path, candidate| candidate.equal?(entry) }
      next unless current_entry && !current_entry.retiring

      current_entry.retiring = true
      owns_retirement = true
      current_entry.client
    end
    return unless client

    Rpc::Diagnostics.log("client_evicted", session: session_path, reason: reason)
    client.close if client.respond_to?(:close)
  ensure
    if owns_retirement
      @mutex.synchronize { @clients.delete_if { |_path, candidate| candidate.equal?(entry) } }
    end
  end

  def new_entry(client)
    Entry.new(client: client, last_used_at: @clock.call, active_requests: 0, operation_mutex: Mutex.new, interrupt_mutex: Mutex.new, retiring: false)
  end

  def touch_entry(entry)
    entry.last_used_at = @clock.call
  end

  def entry_activity_at(entry)
    settled_at = entry.client.settled_at if entry.client.respond_to?(:settled_at)
    [entry.last_used_at, settled_at].compact.max
  end
end
