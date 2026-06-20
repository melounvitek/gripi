# frozen_string_literal: true

module Rpc
  class CommandCatalog
    BUILTIN_COMMANDS = [
      { "name" => "compact", "source" => "other", "description" => "Manually compact context, optional custom instructions" },
      { "name" => "fork", "source" => "other", "description" => "Open the fork picker for this session" },
      { "name" => "tree", "source" => "other", "description" => "Navigate the current session tree" },
      { "name" => "clone", "source" => "other", "description" => "Clone this session and switch to the clone" },
      { "name" => "new", "source" => "other", "description" => "Start a new session in this folder" }
    ].freeze
    INTERNAL_COMMAND_NAMES = %w[pi_web_tree pi_web_tree_leaf].freeze

    def self.commands_from(response)
      new.commands_from(response)
    end

    def self.builtin_commands
      new.builtin_commands
    end

    def commands_from(response)
      (builtin_commands + visible_rpc_commands(response)).uniq { |command| command["name"] }
    end

    def builtin_commands
      BUILTIN_COMMANDS.map(&:dup)
    end

    private

    def visible_rpc_commands(response)
      rpc_commands(response).reject { |command| INTERNAL_COMMAND_NAMES.include?(command["name"]) }
    end

    def rpc_commands(response)
      data = response_data(response)
      commands = data["commands"] if data.is_a?(Hash)
      commands.is_a?(Array) ? commands : []
    end

    def response_data(response)
      response.is_a?(Hash) && response["data"].is_a?(Hash) ? response["data"] : response
    end
  end
end
