require "minitest/autorun"
require_relative "../lib/rpc/command_catalog"

module Rpc
  class CommandCatalogTest < Minitest::Test
    def test_combines_builtin_commands_with_visible_rpc_commands
      commands = CommandCatalog.commands_from(
        "data" => {
          "commands" => [
            { "name" => "review", "source" => "skill", "description" => "Review code" },
            { "name" => "sessions", "source" => "extension", "description" => "Switch, rename, or delete project sessions" },
            { "name" => "rename", "source" => "extension", "description" => "Rename the current session" },
            { "name" => "gripi_tree_navigate", "source" => "extension", "description" => "Internal bridge" },
            { "name" => "gripi_tree_settings", "source" => "extension", "description" => "Internal bridge" },
            { "name" => "gripi_tree_label", "source" => "extension", "description" => "Internal bridge" }
          ]
        }
      )

      assert_equal ["name", "compact", "fork", "tree", "clone", "new", "model", "review", "sessions", "rename"], commands.map { |command| command["name"] }
    end

    def test_builtin_commands_win_when_rpc_command_has_same_name
      commands = CommandCatalog.commands_from(
        "commands" => [
          { "name" => "compact", "source" => "skill", "description" => "Override" }
        ]
      )

      compact = commands.find { |command| command["name"] == "compact" }
      assert_equal "Manually compact context, optional custom instructions", compact["description"]
      assert_equal 1, commands.count { |command| command["name"] == "compact" }
    end

    def test_returns_builtin_commands_for_malformed_rpc_response
      commands = CommandCatalog.commands_from("data" => { "commands" => "broken" })

      assert_equal ["name", "compact", "fork", "tree", "clone", "new", "model"], commands.map { |command| command["name"] }
    end
  end
end
