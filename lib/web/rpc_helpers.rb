require_relative "../pi_rpc_client_registry"
require_relative "../pi_session_store"

module Web
  module RpcHelpers
    private

    def rpc_clients
      settings.rpc_client_registry ||= PiRpcClientRegistry.new(factory: settings.rpc_client_factory.first)
    end

    def cleanup_idle_rpc_clients
      timeout = settings.rpc_idle_timeout_seconds
      return unless timeout.positive?

      rpc_clients.close_idle_clients(
        idle_timeout: timeout,
        except: pending_rpc_cwd_paths
      )
    end

    def with_rpc_client(session_path)
      session_path = canonical_rpc_session_path(session_path)
      rpc_clients.with_client(session_path) { |client| yield client }
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

      real_path = session_file_from(rpc_clients.client_for(session_path).get_state)
      return unless real_path && File.exist?(real_path) && session_cwd(real_path) == cwd

      rpc_clients.move(session_path, real_path)
      attachment_store.migrate_session(session_path, real_path)
      forget_pending_rpc_cwd(session_path)
      real_path
    end

    def remap_pending_rpc_client(session_path)
      return unless File.exist?(session_path)

      pending_path = matching_pending_rpc_path(session_path)
      return unless pending_path

      rpc_clients.move(pending_path, session_path)
      attachment_store.migrate_session(pending_path, session_path)
      forget_pending_rpc_cwd(pending_path)
    end

    def matching_pending_rpc_path(session_path)
      pending_rpc_cwd_entries.find do |pending_path, cwd|
        next unless rpc_clients.active?(pending_path)
        next unless session_cwd(session_path) == cwd

        session_file_from(rpc_clients.client_for(pending_path).get_state) == session_path
      end&.first
    end

    def pending_rpc_cwd(session_path)
      pending_session_registry.cwd_for(session_path)
    end

    def pending_rpc_cwd_paths
      pending_session_registry.paths
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
