module Rpc
  class PendingSessionRegistry
    Entry = Struct.new(:cwd, :created_at, keyword_init: true)

    def initialize(entries = {}, clock: -> { Time.now })
      @clock = clock
      @entries = entries.to_h do |session_path, cwd|
        [session_path, Entry.new(cwd: cwd, created_at: @clock.call)]
      end
      @mutex = Mutex.new
    end

    def remember(session_path, cwd)
      @mutex.synchronize do
        entry = @entries[session_path]
        if entry
          entry.cwd = cwd
        else
          @entries[session_path] = Entry.new(cwd: cwd, created_at: @clock.call)
        end
      end
    end

    def cwd_for(session_path)
      @mutex.synchronize do
        @entries[session_path]&.cwd
      end
    end

    def paths
      @mutex.synchronize do
        @entries.keys
      end
    end

    def entries
      @mutex.synchronize do
        @entries.map { |session_path, entry| [session_path, entry.cwd] }
      end
    end

    def entries_with_created_at
      @mutex.synchronize do
        @entries.map { |session_path, entry| [session_path, entry.cwd, entry.created_at] }
      end
    end

    def forget(session_path)
      @mutex.synchronize do
        @entries.delete(session_path)
      end
    end
  end
end
