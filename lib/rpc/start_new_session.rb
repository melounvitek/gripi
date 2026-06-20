# frozen_string_literal: true

require "securerandom"

module Rpc
  class StartNewSession
    def self.call(cwd, client_factory:, rpc_clients:, pending_sessions:, sessions_root:)
      new(client_factory: client_factory, rpc_clients: rpc_clients, pending_sessions: pending_sessions, sessions_root: sessions_root).call(cwd)
    end

    def initialize(client_factory:, rpc_clients:, pending_sessions:, sessions_root:)
      @client_factory = client_factory
      @rpc_clients = rpc_clients
      @pending_sessions = pending_sessions
      @sessions_root = sessions_root
    end

    def call(cwd)
      client = @client_factory.call(cwd)
      session_path = session_file_from(client.get_state) || pending_session_path
      @rpc_clients.register(session_path, client)
      @pending_sessions.remember(session_path, cwd) unless File.exist?(session_path)
      session_path
    end

    private

    def pending_session_path
      File.join(@sessions_root, "pending-#{SecureRandom.uuid}.jsonl")
    end

    def session_file_from(response)
      data = response_data(response)
      return unless data.is_a?(Hash)

      data["sessionFile"] || data["session_file"] || data["path"]
    end

    def response_data(response)
      response.is_a?(Hash) && response["data"].is_a?(Hash) ? response["data"] : response
    end
  end
end
