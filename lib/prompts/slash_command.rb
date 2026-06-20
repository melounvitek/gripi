module Prompts
  class SlashCommand
    RENAME_PATTERN = %r{\A/(name|rename)(?:[ \t]+([^\r\n]+))?\z}
    COMPACT_PATTERN = %r{\A/compact(?:[ \t]+([^\r\n]+))?\z}
    SIMPLE_COMMANDS = {
      "/fork" => :fork,
      "/tree" => :tree,
      "/clone" => :clone,
      "/new" => :new
    }.freeze

    attr_reader :type, :name, :instructions, :error

    def self.parse(message)
      stripped_message = message.to_s.strip

      if (match = stripped_message.match(RENAME_PATTERN))
        name = match[2]&.strip
        return new(:rename, name: name, error: name ? nil : "Usage: /#{match[1]} <name>")
      end

      if (match = stripped_message.match(COMPACT_PATTERN))
        instructions = match[1]&.strip
        return new(:compact, instructions: instructions.to_s.empty? ? nil : instructions)
      end

      type = SIMPLE_COMMANDS[stripped_message]
      new(type) if type
    end

    def initialize(type, name: nil, instructions: nil, error: nil)
      @type = type
      @name = name
      @instructions = instructions
      @error = error
    end

  end
end
