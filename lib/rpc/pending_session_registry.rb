module Rpc
  class PendingSessionRegistry
    def initialize(entries = {})
      @cwds = entries.dup
      @mutex = Mutex.new
    end

    def remember(session_path, cwd)
      @mutex.synchronize do
        @cwds[session_path] = cwd
      end
    end

    def cwd_for(session_path)
      @mutex.synchronize do
        @cwds[session_path]
      end
    end

    def paths
      @mutex.synchronize do
        @cwds.keys
      end
    end

    def entries
      @mutex.synchronize do
        @cwds.to_a
      end
    end

    def forget(session_path)
      @mutex.synchronize do
        @cwds.delete(session_path)
      end
    end
  end
end
