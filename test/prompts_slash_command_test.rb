require "minitest/autorun"
require_relative "../lib/prompts/slash_command"

class PromptsSlashCommandTest < Minitest::Test
  def test_parses_rename_commands
    command = Prompts::SlashCommand.parse("/name Useful name")

    assert_equal :rename, command.type
    assert_equal "Useful name", command.name
    assert_nil command.error
  end

  def test_parses_bare_rename_as_usage_error
    command = Prompts::SlashCommand.parse("/rename")

    assert_equal :rename, command.type
    assert_nil command.name
    assert_equal "Usage: /rename <name>", command.error
  end

  def test_parses_compact_commands
    command = Prompts::SlashCommand.parse("/compact recent work")

    assert_equal :compact, command.type
    assert_equal "recent work", command.instructions
  end

  def test_parses_bare_compact_without_instructions
    command = Prompts::SlashCommand.parse("/compact")

    assert_equal :compact, command.type
    assert_nil command.instructions
  end

  def test_parses_session_control_commands
    assert_equal :fork, Prompts::SlashCommand.parse("/fork").type
    assert_equal :tree, Prompts::SlashCommand.parse("/tree").type
    assert_equal :clone, Prompts::SlashCommand.parse("/clone").type
    assert_equal :new, Prompts::SlashCommand.parse("/new").type
    assert_equal :model, Prompts::SlashCommand.parse("/model").type
  end

  def test_ignores_multiline_command_like_prompts
    assert_nil Prompts::SlashCommand.parse("/compact recent\nwork")
    assert_nil Prompts::SlashCommand.parse("/rename Useful\nname")
    assert_nil Prompts::SlashCommand.parse("/model openai/gpt-5")
  end

  def test_ignores_regular_prompts
    assert_nil Prompts::SlashCommand.parse("Hello Pi")
  end
end
