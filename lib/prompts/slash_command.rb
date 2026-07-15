module Prompts
  class SlashCommand
    NAME_PATTERN = %r{\A/name(?:[ \t]+([^\r\n]+))?\z}
    COMPACT_PATTERN = %r{\A/compact(?:[ \t]+([^\r\n]+))?\z}
    SIMPLE_COMMANDS = {
      "/fork" => :fork,
      "/tree" => :tree,
      "/clone" => :clone,
      "/new" => :new,
      "/model" => :model
    }.freeze

    attr_reader :type, :name, :instructions

    def self.parse(message)
      stripped_message = message.to_s.strip

      if (match = stripped_message.match(NAME_PATTERN))
        name = match[1]&.strip
        return new(:name, name: name)
      end

      if (match = stripped_message.match(COMPACT_PATTERN))
        instructions = match[1]&.strip
        return new(:compact, instructions: instructions.to_s.empty? ? nil : instructions)
      end

      type = SIMPLE_COMMANDS[stripped_message]
      new(type) if type
    end

    def initialize(type, name: nil, instructions: nil)
      @type = type
      @name = name
      @instructions = instructions
    end

  end
end
