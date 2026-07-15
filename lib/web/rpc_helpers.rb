require "json"
require_relative "../pi_rpc_client_registry"
require_relative "../pi_session_store"
require_relative "../sessions/session_synchronizer"

module Web
  module RpcHelpers
    private

    def rpc_clients
      return settings.rpc_client_registry if settings.rpc_client_registry

      settings.rpc_client_registry_mutex.synchronize do
        settings.rpc_client_registry ||= PiRpcClientRegistry.new(factory: settings.rpc_client_factory.first)
      end
    end

    def session_synchronizer
      registry = rpc_clients
      synchronizer = settings.session_synchronizer
      return synchronizer if synchronizer&.configured_for?(settings.sessions_root, registry)

      settings.session_synchronizer_mutex.synchronize do
        synchronizer = settings.session_synchronizer
        unless synchronizer&.configured_for?(settings.sessions_root, registry)
          synchronizer = Sessions::SessionSynchronizer.new(sessions_root: settings.sessions_root, rpc_clients: registry)
          settings.session_synchronizer = synchronizer
        end
        synchronizer
      end
    end

    def session_sync_state(session_path, include_position: false)
      session_synchronizer.inspect(session_path, include_position: include_position)
    end

    def with_synchronized_rpc_client(session_path)
      return with_rpc_client(session_path) { |client| yield client } unless File.exist?(session_path)

      session_synchronizer.with_mutable_client(session_path) { |client| yield client }
    rescue Sessions::SessionSynchronizer::BlockedError => error
      halt_session_sync_error(error)
    end

    def with_synchronized_interrupt_rpc_client(session_path)
      return with_interrupt_rpc_client(session_path) { |client| yield client } unless File.exist?(session_path)

      session_synchronizer.with_interrupt_client(session_path) { |client| yield client }
    rescue Sessions::SessionSynchronizer::BlockedError => error
      halt_session_sync_error(error)
    end

    def halt_if_session_sync_blocked(session_path)
      return unless File.exist?(session_path)

      state = session_sync_state(session_path)
      return unless state.blocked?

      halt_session_sync_error(
        Sessions::SessionSynchronizer::BlockedError.new(session_sync_error_message(state), mode: state.mode)
      )
    end

    def halt_session_sync_error(error)
      status 409
      content_type :json
      halt JSON.generate(error: error.message, session_sync_mode: error.mode)
    end

    def session_sync_error_message(state)
      session_synchronizer.message_for(state)
    end

    def cleanup_idle_rpc_clients(except: [])
      timeout = settings.rpc_idle_timeout_seconds
      return unless timeout.positive?

      rpc_clients.close_idle_clients(
        idle_timeout: timeout,
        except: except.compact,
        on_close: ->(session_path) { forget_pending_rpc_cwd(session_path) }
      ) do |session_path|
        session_synchronizer.inspect(session_path) if File.exist?(session_path)
      end
    end

    def with_rpc_client(session_path)
      session_path = canonical_rpc_session_path(session_path)
      rpc_clients.with_client(session_path) { |client| yield client }
    end

    def with_interrupt_rpc_client(session_path)
      session_path = canonical_rpc_session_path(session_path)
      rpc_clients.with_interrupt_client(session_path) { |client| yield client }
    end

    def canonical_rpc_session_path(session_path)
      remapped_path = remap_active_pending_rpc_client(session_path)
      return remapped_path if remapped_path

      remap_pending_rpc_client(session_path) unless rpc_clients.active?(session_path)
      session_path
    end

    def remap_active_pending_rpc_client(session_path)
      cwd = pending_rpc_cwd(session_path)
      return unless cwd && rpc_clients.active?(session_path)
      return if multi_user_mode? && !workspace_session_ownership_store.owned_by?(session_path, current_workspace_id)

      state = rpc_clients.with_active_client(session_path) { |client| client.get_state }
      real_path = session_file_from(state)
      return unless real_path && File.exist?(real_path) && session_cwd(real_path) == cwd

      rpc_clients.move(session_path, real_path)
      attachment_store.migrate_session(session_path, real_path)
      claim_session_for_current_workspace(real_path)
      forget_pending_rpc_cwd(session_path)
      real_path
    end

    def remap_pending_rpc_client(session_path)
      return unless File.exist?(session_path)

      pending_path = matching_pending_rpc_path(session_path)
      return unless pending_path
      return if multi_user_mode? && !workspace_session_ownership_store.owned_by?(pending_path, current_workspace_id)

      rpc_clients.move(pending_path, session_path)
      attachment_store.migrate_session(pending_path, session_path)
      claim_session_for_current_workspace(session_path)
      forget_pending_rpc_cwd(pending_path)
    end

    def matching_pending_rpc_path(session_path)
      pending_rpc_cwd_entries.find do |pending_path, cwd|
        next unless rpc_clients.active?(pending_path)
        next unless session_cwd(session_path) == cwd

        state = rpc_clients.with_active_client(pending_path) { |client| client.get_state }
        session_file_from(state) == session_path
      end&.first
    end

    def pending_rpc_cwd(session_path)
      pending_session_registry.cwd_for(session_path)
    end

    def pending_rpc_cwd_entries
      pending_session_registry.entries
    end

    def forget_pending_rpc_cwd(session_path)
      pending_session_registry.forget(session_path)
    end

    def pending_session_registry
      settings.pending_session_registry
    end

    def session_cwd(session_path)
      PiSessionStore.new(root: settings.sessions_root).sessions.find { |session| session.path == session_path }&.cwd
    end

    def response_data(response)
      response.is_a?(Hash) && response["data"].is_a?(Hash) ? response["data"] : response
    end

    def session_file_from(response)
      data = response_data(response)
      return unless data.is_a?(Hash)

      data["sessionFile"] || data["session_file"] || data["path"]
    end
  end
end
