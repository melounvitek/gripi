require "thread"

class PiRpcClientRegistry
  def initialize(factory:)
    @factory = factory
    @clients = {}
    @mutex = Mutex.new
  end

  def ensure_client(session_path)
    @mutex.synchronize do
      @clients[session_path] ||= @factory.call(session_path)
    end
  end

  def register(session_path, client)
    @mutex.synchronize do
      @clients[session_path]&.close unless @clients[session_path].equal?(client)
      @clients[session_path] = client
    end
  end

  def client_for(session_path)
    @mutex.synchronize { @clients[session_path] }
  end

  def active?(session_path)
    !!client_for(session_path)
  end

  def drain_events(session_path)
    client_for(session_path)&.drain_events || []
  end

  def close_all
    clients = @mutex.synchronize do
      existing = @clients.values
      @clients = {}
      existing
    end
    clients.each(&:close)
  end
end
