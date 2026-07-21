require "thread"
require_relative "../pi_session_store"

module Sessions
  class SessionSynchronizer
    Result = Struct.new(:mode, :revision, :append_cursor, :persisted_leaf_id, :rpc_leaf_id, :error, keyword_init: true) do
      def blocked?
        [:external_follow, :conflict].include?(mode)
      end
    end

    State = Struct.new(:snapshot, :mode, :rpc_leaf_id, :error, keyword_init: true)

    class BlockedError < StandardError
      attr_reader :mode

      def initialize(message, mode:)
        @mode = mode
        super(message)
      end
    end

    class BusyError < StandardError; end

    def initialize(sessions_root:, rpc_clients:)
      @sessions_root = sessions_root
      @store = PiSessionStore.new(root: sessions_root)
      @rpc_clients = rpc_clients
      @states = {}
      @locks = {}
      @mutex = Mutex.new
    end

    def configured_for?(sessions_root, rpc_clients)
      @sessions_root == sessions_root && @rpc_clients.equal?(rpc_clients)
    end

    def inspect(session_path, include_position: false)
      synchronize(session_path) { inspect_locked(session_path, include_position: include_position) }
    rescue IOError, Errno::EPIPE
      recover_from_rpc_exit(session_path)
    rescue SystemCallError => error
      conflict_result(session_path, "Session file could not be read: #{error.message}")
    end

    def known_blocked_result(session_path)
      @mutex.synchronize do
        state = @states[session_path]
        result_for(state) if state && [:external_follow, :conflict].include?(state.mode)
      end
    end

    def inspect_if_available(session_path, include_position: false)
      reconcile_if_available(session_path, include_position: include_position)
    end

    def reconcile_if_available(session_path, include_position: false)
      lock = lock_for(session_path)
      acquired = lock.try_lock
      return unless acquired

      result = begin
        inspect_locked(session_path, include_position: include_position)
      rescue PiRpcClientRegistry::OperationPending, PiRpcClientRegistry::ClientRetiring, PiRpcClientRegistry::ClientStarting
        nil
      rescue IOError, Errno::EPIPE
        recover_from_rpc_exit_locked(session_path)
      rescue SystemCallError => error
        conflict_result_locked(session_path, "Session file could not be read: #{error.message}")
      end
      yield result if result && block_given?
      result
    ensure
      lock&.unlock if acquired
    end

    def message_for(result)
      blocked_message(result.mode, result.error)
    end

    def with_mutable_client(session_path, &block)
      lock = lock_for(session_path)
      raise BusyError, "Another session operation is pending." unless lock.try_lock

      begin
        with_verified_client(session_path, :with_client, &block)
      ensure
        lock.unlock
      end
    end

    def with_interrupt_client(session_path)
      lock = lock_for(session_path)
      unless lock.try_lock
        return @rpc_clients.with_existing_interrupt_client(session_path) { |client| yield client }
      end

      begin
        if @rpc_clients.busy?(session_path)
          return @rpc_clients.with_existing_interrupt_client(session_path) { |client| yield client }
        end

        with_verified_client(session_path, :with_interrupt_client) { |client| yield client }
      rescue PiRpcClientRegistry::OperationPending
        @rpc_clients.with_existing_interrupt_client(session_path) { |client| yield client }
      ensure
        lock.unlock
      end
    end

    def with_bash_client(session_path)
      lock = lock_for(session_path)
      raise BusyError, "Another session operation is pending." unless lock.try_lock

      locked = true
      begin
        before = verification_snapshot(session_path)
        @rpc_clients.with_bash_client(session_path) do |client|
          verify_client(session_path, before, client)
          lock.unlock
          locked = false
          yield client
        end
      ensure
        lock.unlock if locked
      end
    end

    def take_over(session_path)
      synchronize(session_path) do
        raise BusyError, "Wait for the gateway task to finish before taking over." if @rpc_clients.busy?(session_path)

        @rpc_clients.close_client_if_idle(session_path)
        before = @store.file_snapshot(session_path)
        raise BlockedError.new("The session file has an incomplete entry.", mode: :conflict) unless before.complete

        takeover_succeeded = false
        begin
          @rpc_clients.with_client(session_path) do |client|
            position = position_for(client, before.append_cursor)
            block_for_position_error!(session_path, before, position)
            block_for_unknown_cursor!(session_path, before, position)

            after = @store.file_snapshot(session_path)
            unless same_file_revision?(before, after) && position[:leaf_id] == before.persisted_leaf_id
              update_state(session_path, after, mode: :external_follow)
              raise BlockedError.new("The session changed while the gateway was taking over. Finish using it in Pi CLI and try again.", mode: :external_follow)
            end

            update_state(session_path, after, mode: :managed, rpc_leaf_id: position[:leaf_id])
            takeover_succeeded = true
          end
        ensure
          @rpc_clients.close_client_if_idle(session_path) unless takeover_succeeded
        end

        result_for(state_for(session_path))
      end
    end

    private

    def with_verified_client(session_path, registry_method)
      before = verification_snapshot(session_path)
      @rpc_clients.public_send(registry_method, session_path) do |client|
        verify_client(session_path, before, client)
        yield client
      end
    end

    def verification_snapshot(session_path)
      result = inspect_locked(session_path)
      raise_blocked(result) if result.blocked?

      state_for(session_path).snapshot
    end

    def verify_client(session_path, before, client)
      position = position_for(client, before.append_cursor)
      block_for_position_error!(session_path, before, position)
      block_for_unknown_cursor!(session_path, before, position)

      after = @store.file_snapshot(session_path)
      unless same_file_revision?(before, after)
        reconciliation = appended_entries_for(client, before.append_cursor)
        result = apply_reconciliation(session_path, state_for(session_path), before, after, reconciliation)
        raise_blocked(result) if result.blocked?
        return
      end

      update_state(session_path, after, mode: :managed, rpc_leaf_id: position[:leaf_id])
    end

    def inspect_locked(session_path, include_position: false)
      snapshot = @store.file_snapshot(session_path)
      state = state_for(session_path)

      unless snapshot.complete
        update_state(session_path, snapshot, mode: :conflict, error: "Session file has an incomplete JSONL entry.")
        retire_stale_client(session_path)
        return result_for(state)
      end

      if state.mode && [:external_follow, :conflict].include?(state.mode)
        update_state(session_path, snapshot, mode: state.mode, error: state.error) unless same_file_revision?(state.snapshot, snapshot)
        retire_stale_client(session_path)
        return result_for(state)
      end

      changed = state.snapshot && !same_file_revision?(state.snapshot, snapshot)
      if changed
        mode, error = external_or_conflict(state.snapshot, snapshot)
        if mode == :conflict
          update_state(session_path, snapshot, mode: mode, error: error)
          retire_stale_client(session_path)
          return result_for(state)
        end

        appended = snapshot.size > state.snapshot.size || snapshot.append_cursor != state.snapshot.append_cursor
        if appended
          unless @rpc_clients.active?(session_path)
            update_state(session_path, snapshot, mode: :external_follow)
            return result_for(state)
          end

          reconciliation = nil
          @rpc_clients.with_existing_client(session_path, touch: false) do |client|
            reconciliation = appended_entries_for(client, state.snapshot.append_cursor)
          end
          return apply_reconciliation(session_path, state, state.snapshot, snapshot, reconciliation)
        end

        update_state(session_path, snapshot, mode: state.mode || :available, rpc_leaf_id: state.rpc_leaf_id)
      end

      if @rpc_clients.active?(session_path) && (include_position || state.mode != :managed)
        update_state(session_path, snapshot, mode: state.mode || :available, rpc_leaf_id: state.rpc_leaf_id) unless state.snapshot
        position = nil
        @rpc_clients.with_existing_client(session_path, touch: false) { |client| position = position_for(client, snapshot.append_cursor) }
        return apply_position(session_path, state, snapshot, position)
      end

      update_state(session_path, snapshot, mode: :available) unless @rpc_clients.active?(session_path)
      result_for(state)
    end

    def apply_reconciliation(session_path, state, previous, current, reconciliation)
      if reconciliation&.[](:error)
        update_state(session_path, current, mode: :conflict, error: reconciliation[:error])
        retire_stale_client(session_path)
        return result_for(state)
      end
      if reconciliation.nil? || !reconciliation[:known]
        update_state(session_path, current, mode: :external_follow)
        retire_stale_client(session_path)
        return result_for(state)
      end

      disk_entry_ids = @store.appended_entry_ids(session_path, previous, current)
      rpc_entry_ids = reconciliation.fetch(:entries).map { |entry| entry["id"] }
      unless rpc_entry_ids.first(disk_entry_ids.length) == disk_entry_ids
        update_state(session_path, current, mode: :external_follow)
        retire_stale_client(session_path)
        return result_for(state)
      end

      update_state(session_path, current, mode: :managed, rpc_leaf_id: reconciliation[:leaf_id])
      result_for(state)
    rescue JSON::ParserError, KeyError, TypeError, SystemCallError => error
      update_state(session_path, current, mode: :conflict, error: "Session append reconciliation failed: #{error.message}")
      retire_stale_client(session_path)
      result_for(state)
    end

    def apply_position(session_path, state, snapshot, position)
      if position&.[](:error)
        update_state(session_path, snapshot, mode: :conflict, error: position[:error])
        retire_stale_client(session_path)
      elsif position.nil? || !position[:known]
        update_state(session_path, snapshot, mode: :external_follow)
        retire_stale_client(session_path)
      else
        update_state(session_path, snapshot, mode: :managed, rpc_leaf_id: position[:leaf_id])
      end
      result_for(state)
    end

    def external_or_conflict(previous, current)
      return [:conflict, "Session file was replaced or truncated."] if previous.device != current.device || previous.inode != current.inode || current.size < previous.size
      return [:conflict, "Session file changed without a complete appended entry."] if current.size > previous.size && current.append_cursor == previous.append_cursor

      [:external_follow, nil]
    end

    def appended_entries_for(client, append_cursor)
      return { known: false, leaf_id: nil, entries: [], error: "Installed Pi does not support session synchronization." } unless client.respond_to?(:session_entries_after)

      client.session_entries_after(append_cursor)
    end

    def position_for(client, append_cursor)
      return { known: false, leaf_id: nil, error: "Installed Pi does not support session synchronization." } unless client.respond_to?(:session_position)

      client.session_position(append_cursor)
    end

    def block_for_position_error!(session_path, snapshot, position)
      return unless position[:error]

      update_state(session_path, snapshot, mode: :conflict, error: position[:error])
      raise BlockedError.new(blocked_message(:conflict, position[:error]), mode: :conflict)
    end

    def block_for_unknown_cursor!(session_path, snapshot, position)
      return if position[:known]

      update_state(session_path, snapshot, mode: :external_follow)
      raise BlockedError.new(blocked_message(:external_follow), mode: :external_follow)
    end

    def update_state(session_path, snapshot, mode:, rpc_leaf_id: nil, error: nil)
      state = state_for(session_path)
      state.snapshot = snapshot
      state.mode = mode
      state.rpc_leaf_id = rpc_leaf_id
      state.error = error
    end

    def state_for(session_path)
      @states[session_path] ||= State.new
    end

    def result_for(state)
      snapshot = state.snapshot
      Result.new(
        mode: state.mode || :available,
        revision: snapshot&.revision,
        append_cursor: snapshot&.append_cursor,
        persisted_leaf_id: snapshot&.persisted_leaf_id,
        rpc_leaf_id: state.rpc_leaf_id,
        error: state.error
      )
    end

    def recover_from_rpc_exit(session_path)
      synchronize(session_path) { recover_from_rpc_exit_locked(session_path) }
    end

    def recover_from_rpc_exit_locked(session_path)
      snapshot = @store.file_snapshot(session_path)
      state = state_for(session_path)
      mode = state.snapshot && !same_file_revision?(state.snapshot, snapshot) ? :external_follow : :available
      update_state(session_path, snapshot, mode: mode)
      result_for(state)
    rescue SystemCallError => error
      conflict_result_locked(session_path, "Session file could not be read: #{error.message}")
    end

    def conflict_result(session_path, message)
      synchronize(session_path) { conflict_result_locked(session_path, message) }
    end

    def conflict_result_locked(session_path, message)
      state = state_for(session_path)
      state.mode = :conflict
      state.error = message
      result_for(state)
    end

    def same_file_revision?(left, right)
      left&.revision == right&.revision
    end

    def retire_stale_client(session_path)
      @rpc_clients.close_client_if_idle(session_path)
    end

    def raise_blocked(result)
      raise BlockedError.new(blocked_message(result.mode, result.error), mode: result.mode)
    end

    def blocked_message(mode, error = nil)
      return "Session synchronization failed: #{error}" if mode == :conflict

      "This session changed outside the gateway. Finish using it in Pi CLI, then take over in the gateway."
    end

    def synchronize(session_path, &block)
      lock_for(session_path).synchronize(&block)
    end

    def lock_for(session_path)
      @mutex.synchronize { @locks[session_path] ||= Mutex.new }
    end
  end
end
