# frozen_string_literal: true

module Rpc
  class BranchSession
    def self.call(previous_session_path, rpc_clients:, pending_sessions:, cwd:)
      new(rpc_clients: rpc_clients, pending_sessions: pending_sessions).call(previous_session_path, cwd)
    end

    def initialize(rpc_clients:, pending_sessions:)
      @rpc_clients = rpc_clients
      @pending_sessions = pending_sessions
    end

    def call(previous_session_path, cwd)
      new_session_path = session_file_from(client_for(previous_session_path)&.get_state) || previous_session_path
      move_client(previous_session_path, new_session_path, cwd) if new_session_path != previous_session_path
      new_session_path
    end

    private

    def client_for(session_path)
      @rpc_clients.client_for(session_path)
    end

    def move_client(previous_session_path, new_session_path, cwd)
      @rpc_clients.move(previous_session_path, new_session_path)
      @pending_sessions.remember(new_session_path, cwd) unless File.exist?(new_session_path)
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
