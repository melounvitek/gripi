require "minitest/autorun"
require_relative "../lib/rpc/command_catalog"

module Rpc
  class CommandCatalogTest < Minitest::Test
    def test_combines_builtin_commands_with_visible_rpc_commands
      commands = CommandCatalog.commands_from(
        "data" => {
          "commands" => [
            { "name" => "review", "source" => "skill", "description" => "Review code" },
            { "name" => "pi_web_tree", "source" => "extension", "description" => "Internal bridge" },
            { "name" => "pi_web_tree_leaf", "source" => "extension", "description" => "Internal bridge" }
          ]
        }
      )

      assert_equal ["compact", "fork", "tree", "clone", "new", "review"], commands.map { |command| command["name"] }
    end

    def test_builtin_commands_win_when_rpc_command_has_same_name
      commands = CommandCatalog.commands_from(
        "commands" => [
          { "name" => "compact", "source" => "skill", "description" => "Override" }
        ]
      )

      assert_equal "Manually compact context, optional custom instructions", commands.first["description"]
      assert_equal 1, commands.count { |command| command["name"] == "compact" }
    end

    def test_returns_builtin_commands_for_malformed_rpc_response
      commands = CommandCatalog.commands_from("data" => { "commands" => "broken" })

      assert_equal ["compact", "fork", "tree", "clone", "new"], commands.map { |command| command["name"] }
    end
  end
end
