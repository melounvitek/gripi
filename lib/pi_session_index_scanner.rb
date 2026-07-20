require_relative "pi_bash_execution_text"

class PiSessionIndexScanner
  CAPTURE_BYTES = 8 * 1024
  STRING_SPECIAL_BYTE = /["\\\x00-\x1f]/n
  MAX_TRACKED_VALUES = 2_000
  MAX_NESTING_DEPTH = 100
  MAX_SCALAR_TOKEN_BYTES = 128
  Frame = Struct.new(:kind, :path, :state, :key, :index, :keys, keyword_init: true)

  class StringStats
    HOME = Dir.home.b.freeze
    attr_reader :bytes, :characters, :non_whitespace_bytes

    def initialize
      @bytes = 0
      @characters = 0
      @non_whitespace_bytes = 0
      @capture = +"".b
      @truncated = false
      @escape = false
      @unicode = nil
      @high_surrogate = nil
      @home_window = +"".b
      @home_occurrences = 0
      @valid = true
    end

    def consume_plain(string)
      return invalid! if @unicode || @escape || @high_surrogate

      track_home_occurrences_in(string)
      @bytes += string.bytesize
      @characters += string.count("\x00-\x7F\xC0-\xFF".b)
      @non_whitespace_bytes += string.count("^\x00\x09\x0A\x0B\x0C\x0D\x20".b)
      if !@truncated && @capture.bytesize + string.bytesize <= CAPTURE_BYTES
        @capture << string
      elsif !@truncated
        remaining = CAPTURE_BYTES - @capture.bytesize
        @capture << string.byteslice(0, remaining) if remaining.positive?
        @truncated = true
      end
    end

    def consume(byte)
      if @unicode
        return invalid! unless hex?(byte)

        @unicode << byte
        emit_codepoint(@unicode.to_i(16)) if @unicode.length == 4
        return
      end

      if @escape
        @escape = false
        if byte == 117 # u
          @unicode = +""
        else
          return invalid! if @high_surrogate

          decoded = { 34 => 34, 92 => 92, 47 => 47, 98 => 8, 102 => 12, 110 => 10, 114 => 13, 116 => 9 }[byte]
          decoded ? emit_bytes(decoded.chr, whitespace_byte?(decoded) ? 0 : 1, 1) : invalid!
        end
        return
      end

      if byte == 92
        @escape = true
      elsif byte < 0x20
        invalid!
      else
        emit_bytes(byte.chr, whitespace_byte?(byte) ? 0 : 1, continuation_byte?(byte) ? 0 : 1)
      end
    end

    def finish
      invalid! if @escape || @unicode || @high_surrogate
      @capture.force_encoding(Encoding::UTF_8)
      if @truncated && !@capture.valid_encoding?
        3.times do
          @capture = @capture.byteslice(0, @capture.bytesize - 1)
          break if @capture.valid_encoding?
        end
      end
      invalid! unless @capture.valid_encoding?
      self
    end

    def valid?
      @valid
    end

    def empty?
      @bytes.zero?
    end

    def value
      @capture unless @truncated
    end

    def prefix
      @capture
    end

    def home_replacement_lower_bytes
      [bytes - (@home_occurrences * [HOME.bytesize - 1, 0].max), 0].max
    end

    private

    def emit_codepoint(codepoint)
      @unicode = nil
      if codepoint.between?(0xD800, 0xDBFF)
        return invalid! if @high_surrogate

        @high_surrogate = codepoint
        return
      end
      if codepoint.between?(0xDC00, 0xDFFF)
        return invalid! unless @high_surrogate

        codepoint = 0x10000 + ((@high_surrogate - 0xD800) << 10) + codepoint - 0xDC00
        @high_surrogate = nil
      elsif @high_surrogate
        return invalid!
      end

      string = codepoint.chr(Encoding::UTF_8)
      whitespace = codepoint <= 0x20 && whitespace_byte?(codepoint)
      emit_bytes(string, whitespace ? 0 : string.bytesize, 1)
    rescue RangeError
      invalid!
    end

    def emit_bytes(string, non_whitespace_bytes, characters)
      track_home_occurrences(string)
      @bytes += string.bytesize
      @characters += characters
      @non_whitespace_bytes += non_whitespace_bytes
      if !@truncated && @capture.bytesize + string.bytesize <= CAPTURE_BYTES
        @capture << string
      else
        @truncated = true
      end
    end

    def track_home_occurrences(string)
      return if HOME.empty?

      @home_window << string
      @home_window = @home_window.byteslice(-HOME.bytesize, HOME.bytesize) if @home_window.bytesize > HOME.bytesize
      @home_occurrences += 1 if @home_window == HOME
    end

    def track_home_occurrences_in(string)
      return if HOME.empty?

      combined = @home_window + string
      offset = 0
      while (index = combined.index(HOME, offset))
        @home_occurrences += 1
        offset = index + HOME.bytesize
      end
      retained = [HOME.bytesize - 1, 0].max
      @home_window = retained.zero? ? +"".b : (combined.byteslice(-retained, retained) || combined)
    end

    def whitespace_byte?(byte)
      byte.zero? || byte.between?(9, 13) || byte == 32
    end

    def continuation_byte?(byte)
      (byte & 0xC0) == 0x80
    end

    def hex?(byte)
      byte.between?(48, 57) || byte.between?(65, 70) || byte.between?(97, 102)
    end

    def invalid!
      @valid = false
    end
  end

  class Collector
    TOP_LEVEL_KEYS = {
      "message" => %w[type id parentId timestamp message],
      "compaction" => %w[type id parentId timestamp summary firstKeptEntryId tokensBefore details fromHook],
      "branch_summary" => %w[type id parentId timestamp fromId summary details fromHook],
      "custom_message" => %w[type customType content display details id parentId timestamp]
    }.freeze
    REQUIRED_TOP_LEVEL_KEYS = {
      "message" => %w[type id parentId timestamp message],
      "compaction" => %w[type id parentId timestamp summary firstKeptEntryId tokensBefore],
      "branch_summary" => %w[type id parentId timestamp fromId summary],
      "custom_message" => %w[type customType content display id parentId timestamp]
    }.freeze
    PART_KEYS = {
      "text" => %w[type text textSignature],
      "thinking" => %w[type thinking thinkingSignature redacted],
      "toolCall" => %w[type id name arguments thoughtSignature],
      "toolResult" => %w[type output result toolName name],
      "image" => %w[type data mimeType]
    }.freeze

    def initialize
      @keys = {}
      @strings = {}
      @scalars = {}
      @containers = {}
      @argument_bytes = Hash.new(0)
      @argument_commands = {}
      @argument_timeouts = {}
      @general_tool_names = {}
      @general_tool_arguments = Hash.new { |hash, key| hash[key] = {} }
      @general_tool_output_non_whitespace_bytes = 0
      @all_json_minimum_bytes = 0
      @invalid_general_details = false
      @tracked_values = 0
      @valid = true
    end

    def start_container(path, kind)
      return unless @valid

      @all_json_minimum_bytes += 1
      if interesting_container?(path)
        return unless reserve_tracked_value
        @containers[path] = kind
      end
      @invalid_general_details = true if invalid_general_value_path?(path)
      if path.length == 4 && path[0, 3] == ["message", "details", "tools"]
        @invalid_general_details = true unless kind == :object
      end
    end

    def object_key(path, key, stats)
      return unless @valid

      @all_json_minimum_bytes += stats.bytes
      if canonical_key_path?(path)
        return unless reserve_tracked_value
        (@keys[path] ||= []) << key
      end
    end

    def string(path, stats)
      return unless @valid
      return invalid! unless stats.valid?

      @all_json_minimum_bytes += stats.bytes
      @invalid_general_details = true if path.length == 4 && path[0, 3] == ["message", "details", "tools"]
      if argument_path?(path)
        @argument_bytes[path[2]] += stats.bytes
        @argument_commands[path[2]] = stats if path.length == 5 && path[4] == "command"
        @argument_timeouts[path[2]] = stats if path.length == 5 && path[4] == "timeout"
      elsif path == ["message", "details", "model"]
        return unless reserve_tracked_value
        @general_model = stats
      elsif path.length == 5 && path[0, 3] == ["message", "details", "tools"] && path[4] == "name"
        return unless reserve_tracked_value
        @general_tool_names[path[3]] = stats.value
        @invalid_general_details = true unless stats.value
      elsif general_tool_argument_path?(path)
        return unless reserve_tracked_value
        @general_tool_arguments[path[3]][path[5]] = stats
      elsif path.length == 5 && path[0, 3] == ["message", "details", "tools"] && path[4] == "output"
        @general_tool_output_non_whitespace_bytes += stats.non_whitespace_bytes
      elsif path.length == 4 && path[0, 3] == ["message", "details", "textItems"]
        @last_text_item_stats = stats
      end
      if interesting_string?(path)
        return unless reserve_tracked_value
        @strings[path] = stats
      end
    end

    def scalar(path, value)
      return unless @valid

      @all_json_minimum_bytes += 1
      @invalid_general_details = true if invalid_general_value_path?(path) || (path.length == 4 && path[0, 3] == ["message", "details", "tools"])
      if interesting_scalar?(path)
        return unless reserve_tracked_value
        @scalars[path] = value
      end
    end

    def finish
      return unless @valid

      type = string_value(["type"])
      return unless canonical_top_level?(type)

      case type
      when "message" then message_metadata
      when "compaction" then compaction_metadata
      when "branch_summary" then branch_summary_metadata
      when "custom_message" then custom_message_metadata
      end
    end

    def track_object_keys?(path)
      canonical_key_path?(path)
    end

    private

    def message_metadata
      role = string_value(["message", "role"])
      return unless canonical_message?(role)
      return unless exact_string?(["id"]) && exact_nullable_string?(["parentId"])
      if role == "toolResult"
        return unless exact_string?(["message", "toolCallId"]) && exact_string?(["message", "toolName"])
      end
      return bash_execution_metadata if role == "bashExecution"
      if role == "assistant" && @keys[["message"]] != %w[role content]
        return unless exact_string?(["message", "provider"]) && exact_string?(["message", "model"])
      end

      direct_content = @strings[["message", "content"]]
      parts = if role == "user" && direct_content
        [{ index: 0, direct: direct_content }]
      else
        content_parts(["message", "content"])
      end
      return unless parts

      segments = case role
      when "assistant" then assistant_segments(parts)
      when "user", "toolResult" then non_assistant_segments(role, parts)
      else return
      end
      return unless segments

      {
        type: "message",
        id: string_value(["id"]),
        parent_id: nullable_string(["parentId"]),
        target_id: nil,
        role: role,
        segments: segments,
        subagent_tool_call_ids: parts.filter_map do |part|
          part[:id] if role == "assistant" && part[:type] == "toolCall" && part[:name] == "subagent" && !part[:id].to_s.empty?
        end,
        status_data: assistant_status_data(role),
        estimate_text_length: estimate_content_characters(parts)
      }
    end

    def bash_execution_metadata
      command = @strings[["message", "command"]]
      output = @strings[["message", "output"]]
      return unless command && output
      return unless @scalars[["message", "timestamp"]].is_a?(Numeric)
      return unless boolean_scalar?(["message", "cancelled"]) && boolean_scalar?(["message", "truncated"])
      return unless optional_integer_scalar?(["message", "exitCode"])
      return unless optional_boolean_scalar?(["message", "excludeFromContext"])
      return unless !@keys[["message"]].include?("fullOutputPath") || @strings.key?(["message", "fullOutputPath"])

      exit_code = @scalars[["message", "exitCode"]]
      cancelled = @scalars[["message", "cancelled"]] == true
      truncated = @scalars[["message", "truncated"]] == true
      full_output_path = @strings[["message", "fullOutputPath"]]
      full_output_path_characters = full_output_path.characters if full_output_path && !full_output_path.empty?
      {
        type: "message",
        id: string_value(["id"]),
        parent_id: nullable_string(["parentId"]),
        target_id: nil,
        role: "bashExecution",
        segments: [segment("bashExecution", (command.bytes + output.bytes) * 2, nil, "bash")],
        subagent_tool_call_ids: [],
        status_data: { type: "bash_execution", excluded_from_context: @scalars[["message", "excludeFromContext"]] == true },
        estimate_text_length: @scalars[["message", "excludeFromContext"]] == true ? nil : PiBashExecutionText.character_length(
          command_characters: command.characters,
          output_characters: output.characters,
          output_empty: output.empty?,
          exit_code: exit_code,
          cancelled: cancelled,
          truncated: truncated,
          full_output_path_characters: full_output_path_characters
        )
      }
    end

    def compaction_metadata
      return unless exact_string?(["id"]) && exact_nullable_string?(["parentId"]) && exact_string?(["firstKeptEntryId"])

      summary = @strings[["summary"]]
      return unless summary
      minimum = summary.non_whitespace_bytes.zero? ? @all_json_minimum_bytes * 2 : minimum_bytes(summary)
      {
        type: "compaction",
        id: string_value(["id"]),
        parent_id: nullable_string(["parentId"]),
        target_id: nil,
        role: nil,
        segments: [segment("status", minimum)],
        subagent_tool_call_ids: [],
        status_data: {
          type: "compaction",
          summary_length: summary&.characters,
          first_kept_entry_id: string_value(["firstKeptEntryId"])
        },
        estimate_text_length: nil
      }
    end

    def branch_summary_metadata
      return unless exact_string?(["id"]) && exact_nullable_string?(["parentId"]) && exact_string?(["fromId"]) && @strings[["summary"]]

      base_metadata("branch_summary")
    end

    def custom_message_metadata
      display = @scalars[["display"]]
      return unless display == true || display == false
      return unless exact_string?(["id"]) && exact_nullable_string?(["parentId"]) && exact_string?(["customType"])

      content_minimum = custom_content_minimum
      return if content_minimum.nil?

      segments = display ? [segment("custom", content_minimum)] : []
      base_metadata("custom_message").merge(segments: segments)
    end

    def base_metadata(type)
      {
        type: type,
        id: string_value(["id"]),
        parent_id: nullable_string(["parentId"]),
        target_id: nil,
        role: nil,
        segments: [],
        subagent_tool_call_ids: [],
        status_data: nil,
        estimate_text_length: nil
      }
    end

    def assistant_segments(parts)
      segments = []
      plain_parts = []
      flush_plain = lambda do
        unless plain_parts.empty?
          values = plain_parts.filter_map { |part| content_value_stats(part) }
          characters = values.sum(&:characters) + [values.length - 1, 0].max
          segments << segment("assistant", values.sum(&:bytes) * 2) if characters.positive?
          plain_parts.clear
        end
      end

      parts.each do |part|
        if part[:type] == "toolCall" && part[:name] == "subagent"
          next
        elsif part[:type] == "thinking"
          flush_plain.call
          stats = rendered_thinking_stats(part[:thinking])
          return unless stats
          segments << segment("assistant", minimum_bytes(stats)) unless stats.empty?
        elsif %w[toolCall toolResult].include?(part[:type])
          flush_plain.call
          minimum = if part[:type] == "toolCall" && part[:name] == "bash"
            command_bytes = @argument_commands[part[:index]]&.home_replacement_lower_bytes.to_i
            timeout_bytes = @argument_timeouts[part[:index]]&.bytes.to_i
            (command_bytes + timeout_bytes) * 2
          elsif part[:type] == "toolCall" && !%w[bash read edit write].include?(part[:name])
            @argument_bytes[part[:index]] * 2
          end
          segments << segment("assistant", minimum, part[:id], part[:name] || part[:tool_name])
        else
          plain_parts << part
        end
      end
      flush_plain.call
      segments
    end

    def non_assistant_segments(role, parts)
      tool_name = string_value(["message", "toolName"])
      tool_call_id = string_value(["message", "toolCallId"])
      image_bytes = parts.sum { |part| valid_image?(part) ? part[:data].bytes : 0 }
      general_subagent = tool_name == "subagent" && @containers[["message", "details", "tools"]] == :array && @containers[["message", "details", "usage"]] == :object
      if general_subagent && @invalid_general_details
        return [segment(role, nil, tool_call_id, tool_name)]
      end
      text_stats = []
      parts.each do |part|
        stats = content_value_stats(part)
        if part[:type] == "thinking"
          stats = rendered_thinking_stats(stats)
          return unless stats
        end
        text_stats << stats if stats
      end
      text_characters = text_stats.sum(&:characters) + [text_stats.length - 1, 0].max
      diff = @strings[["message", "details", "diff"]]
      visible = text_characters.positive? || image_bytes.positive? || general_subagent || (tool_name == "edit" && diff && !diff.empty?)
      return [] unless visible

      text_minimum = if general_subagent
        final_stats = @strings[["message", "details", "streamingText"]]
        final_stats = @last_text_item_stats if !final_stats || final_stats.empty?
        final_non_whitespace_bytes = if final_stats && !final_stats.empty?
          final_stats.bytes
        else
          text_stats.sum(&:bytes)
        end
        visible_argument_bytes = general_tool_visible_argument_bytes
        model_bytes = @general_model&.bytes.to_i
        (@general_tool_output_non_whitespace_bytes + final_non_whitespace_bytes + visible_argument_bytes + model_bytes) * 2
      elsif tool_name == "edit" && diff && !diff.empty?
        diff.bytes * 2
      elsif role == "user"
        nil
      else
        text_stats.sum(&:bytes) * 2
      end
      minimum = text_minimum && text_minimum + image_bytes
      paired_minimum = tool_name == "read" && @scalars[["message", "isError"]] != true ? image_bytes : minimum
      [segment(role, minimum, tool_call_id, tool_name, paired_minimum)]
    end

    def assistant_status_data(role)
      return unless role == "assistant"

      usage = {}
      @scalars.each do |path, value|
        next unless path[0, 2] == ["message", "usage"]

        if path.length == 3
          usage[path[2]] = value
        elsif path == ["message", "usage", "cost", "total"]
          (usage["cost"] ||= {})["total"] = value
        end
      end
      {
        type: "assistant",
        provider: string_value(["message", "provider"]),
        model_id: string_value(["message", "model"]),
        usage: @containers[["message", "usage"]] == :object ? usage : nil,
        stop_reason: string_value(["message", "stopReason"])
      }
    end

    def estimate_content_characters(parts)
      values = []
      parts.each do |part|
        stats = content_value_stats(part)
        return nil if %w[toolCall toolResult].include?(part[:type])
        next unless stats

        if part[:type] == "thinking" && stats.value
          text = stats.value.sub(/\A\s*\*\*[^\n*][^\n]*\*\*\s*\n{2,}/, "")
          values << text.length
        elsif part[:type] == "thinking"
          return nil
        else
          values << stats.characters
        end
      end
      values.sum + [values.length - 1, 0].max
    end

    def content_parts(root)
      return [] unless @containers[root] == :array

      indexes = (@strings.keys + @keys.keys + @containers.keys + @scalars.keys).filter_map do |path|
        path[root.length] if path[0, root.length] == root && path[root.length].is_a?(Integer)
      end.uniq.sort
      indexes.map do |index|
        direct = @strings[root + [index]]
        next({ index: index, direct: direct }) if direct
        return unless @containers[root + [index]] == :object

        type = string_value(root + [index, "type"])
        return unless canonical_part?(root + [index], type)
        if type == "toolCall"
          return unless exact_string?(root + [index, "id"]) && exact_string?(root + [index, "name"])
        elsif type == "image"
          return unless exact_string?(root + [index, "mimeType"])
        end
        {
          index: index,
          type: type,
          id: string_value(root + [index, "id"]),
          name: string_value(root + [index, "name"]),
          tool_name: string_value(root + [index, "toolName"]),
          text: @strings[root + [index, "text"]],
          thinking: @strings[root + [index, "thinking"]],
          output: @strings[root + [index, "output"]],
          result: @strings[root + [index, "result"]],
          data: @strings[root + [index, "data"]],
          mime_type: string_value(root + [index, "mimeType"])
        }
      end
    end

    def content_value_stats(part)
      return part[:direct] if part[:direct]
      return part[:text] if part[:text]
      return part[:thinking] if part[:type] == "thinking"
      return part[:output] || part[:result] if part[:type] == "toolResult"
      return nil if part[:type] == "image"
      return nil if part[:type] == "toolCall" && part[:name] == "bash"
      return nil if part[:type] == "toolCall" && %w[read].include?(part[:name])
      return StringStats.new.finish if part[:type] == "toolCall" && %w[edit write].include?(part[:name])
      return synthetic_stats("[tool]") if part[:type] == "toolCall"

      nil
    end

    def synthetic_stats(value)
      StringStats.new.tap { |stats| stats.consume_plain(value.b) }.finish
    end

    def rendered_thinking_stats(stats)
      return unless stats
      return synthetic_stats(stats.value.sub(/\A\s*\*\*[^\n*][^\n]*\*\*\s*\n{2,}/, "")) if stats.value

      after_leading_whitespace = stats.prefix.sub(/\A\s*/, "")
      return if after_leading_whitespace.empty? || after_leading_whitespace == "*" || after_leading_whitespace.start_with?("**")

      stats
    end

    def custom_content_minimum
      direct = @strings[["content"]]
      return direct.bytes * 2 if direct
      return unless @containers[["content"]] == :array

      parts = content_parts(["content"])
      return unless parts && parts.all? { |part| %w[text image].include?(part[:type]) }

      text_bytes = parts.sum { |part| part[:type] == "text" ? part[:text].bytes : 0 }
      image_bytes = parts.sum { |part| valid_image?(part) ? part[:data].bytes : 0 }
      (text_bytes * 2) + image_bytes
    end

    def minimum_bytes(stats)
      stats&.non_whitespace_bytes&.*(2)
    end

    def segment(role, minimum, tool_call_id = nil, tool_name = nil, paired_minimum = minimum)
      {
        role: role,
        tool_call_id: tool_call_id,
        tool_name: tool_name,
        minimum_window_bytes: minimum,
        paired_minimum_window_bytes: paired_minimum
      }
    end

    def valid_image?(part)
      part[:type] == "image" && %w[image/png image/jpeg image/gif image/webp].include?(part[:mime_type]) && part[:data] && !part[:data].empty?
    end

    def canonical_top_level?(type)
      expected = TOP_LEVEL_KEYS[type]
      required = REQUIRED_TOP_LEVEL_KEYS[type]
      expected && ordered_subset?(@keys[[]], expected) && (required - Array(@keys[[]])).empty?
    end

    def canonical_message?(role)
      keys = @keys[["message"]]
      case role
      when "user"
        keys == %w[role content] || keys == %w[role content timestamp]
      when "toolResult"
        ordered_subset?(keys, %w[role toolCallId toolName content details addedToolNames isError timestamp]) &&
          Array(keys)[0, 4] == %w[role toolCallId toolName content] && keys.include?("isError")
      when "assistant"
        canonical_assistant_keys?(keys)
      when "bashExecution"
        ordered_subset?(keys, %w[role command output exitCode cancelled truncated fullOutputPath timestamp excludeFromContext]) &&
          (keys & %w[role command output cancelled truncated timestamp]) == %w[role command output cancelled truncated timestamp]
      else
        false
      end
    end

    def canonical_assistant_keys?(keys)
      return true if keys == %w[role content]
      return false unless Array(keys)[0, 8] == %w[role content api provider model usage stopReason timestamp]

      tail = keys[8..]
      tail.uniq.length == tail.length && tail.all? { |key| %w[responseModel responseId diagnostics errorMessage].include?(key) }
    end

    def canonical_part?(path, type)
      expected = PART_KEYS[type]
      required = {
        "text" => %w[type text],
        "thinking" => %w[type thinking],
        "toolCall" => %w[type id name arguments],
        "toolResult" => %w[type],
        "image" => %w[type data mimeType]
      }[type]
      expected && ordered_subset?(@keys[path], expected) && (required - @keys[path]).empty?
    end

    def ordered_subset?(actual, expected)
      return false unless actual && actual.uniq.length == actual.length

      positions = actual.map { |key| expected.index(key) }
      positions.none?(&:nil?) && positions.each_cons(2).all? { |left, right| left < right }
    end

    def boolean_scalar?(path)
      @scalars[path] == true || @scalars[path] == false
    end

    def optional_boolean_scalar?(path)
      !@keys[["message"]].include?(path.last) || boolean_scalar?(path)
    end

    def optional_integer_scalar?(path)
      return true unless @keys[["message"]].include?(path.last)

      return false unless @scalars.key?(path)

      value = @scalars[path]
      value.nil? || value.is_a?(Integer)
    end

    def exact_string?(path)
      @strings[path]&.value
    end

    def exact_nullable_string?(path)
      exact_string?(path) || (@scalars.key?(path) && @scalars[path].nil?)
    end

    def nullable_string(path)
      return string_value(path) if @strings.key?(path)
      return nil if @scalars[path].nil?

      invalid!
      nil
    end

    def string_value(path)
      @strings[path]&.value
    end

    def canonical_key_path?(path)
      path.empty? || path == ["message"] || (path.length == 3 && path[0, 2] == ["message", "content"]) || (path.length == 2 && path[0] == "content" && path[1].is_a?(Integer))
    end

    def interesting_container?(path)
      path.empty? || path == ["message"] || path == ["message", "content"] || path == ["content"] ||
        (path.length == 3 && path[0, 2] == ["message", "content"]) ||
        (path.length == 2 && path[0] == "content") ||
        [["message", "usage"], ["message", "details"], ["message", "details", "tools"], ["message", "details", "usage"],
         ["message", "details", "textItems"], ["message", "details", "streamingText"]].include?(path)
    end

    def interesting_string?(path)
      return true if path.length == 1 && %w[type id parentId targetId timestamp summary firstKeptEntryId customType fromId].include?(path[0])
      return true if path[0, 1] == ["content"] && (path.length <= 3)
      return true if path[0, 2] == ["message", "content"] && path.length <= 4
      return true if path.length == 2 && path[0] == "message" && %w[role toolCallId toolName provider model stopReason command output fullOutputPath].include?(path[1])
      return true if path == ["message", "details", "diff"] || path == ["message", "details", "streamingText"] || path == ["message", "details", "model"]

      false
    end

    def interesting_scalar?(path)
      custom_content_item = path.length == 2 && path[0] == "content" && path[1].is_a?(Integer)
      message_content_item = path.length == 3 && path[0, 2] == ["message", "content"] && path[2].is_a?(Integer)
      path == ["parentId"] || path == ["display"] || path == ["message", "isError"] ||
        (path.length == 2 && path[0] == "message" && %w[exitCode cancelled truncated timestamp excludeFromContext fullOutputPath].include?(path[1])) ||
        path[0, 2] == ["message", "usage"] || custom_content_item || message_content_item
    end

    def invalid_general_value_path?(path)
      path == ["message", "details", "streamingText"] || path == ["message", "details", "model"] ||
        (path.length == 4 && path[0, 3] == ["message", "details", "textItems"]) ||
        (path.length == 5 && path[0, 3] == ["message", "details", "tools"] && %w[name output].include?(path[4])) ||
        general_tool_argument_path?(path)
    end

    def general_tool_visible_argument_bytes
      @general_tool_arguments.sum do |index, arguments|
        case @general_tool_names[index]
        when "bash"
          arguments["command"]&.bytes.to_i
        when "read", "write", "edit", "ls"
          (arguments["path"] || arguments["file_path"])&.bytes.to_i
        when "grep"
          arguments["pattern"]&.bytes.to_i + arguments["path"]&.bytes.to_i
        when "find"
          arguments["pattern"]&.bytes.to_i + arguments["path"]&.bytes.to_i
        else
          0
        end
      end
    end

    def general_tool_argument_path?(path)
      path.length == 6 && path[0, 3] == ["message", "details", "tools"] && path[4] == "args" &&
        %w[command path file_path pattern].include?(path[5])
    end

    def argument_path?(path)
      path.length >= 4 && path[0, 2] == ["message", "content"] && path[2].is_a?(Integer) && path[3] == "arguments"
    end

    def reserve_tracked_value
      @tracked_values += 1
      return true if @tracked_values <= MAX_TRACKED_VALUES

      invalid!
      false
    end

    def invalid!
      @valid = false
    end
  end

  def initialize
    @collector = Collector.new
    @stack = []
    @root_state = :value
    @token = nil
    @structural_keys = 0
    @valid = true
  end

  def feed(chunk)
    index = 0
    while @valid && index < chunk.bytesize
      if @token == :string && !@string_escaped && !@string_unicode
        special_index = chunk.index(STRING_SPECIAL_BYTE, index)
        plain_end = special_index || chunk.bytesize
        if plain_end > index
          @string_stats.consume_plain(chunk.byteslice(index, plain_end - index))
          index = plain_end
          next
        end
      end

      byte = chunk.getbyte(index)
      consumed = consume(byte)
      index += 1 if consumed
    end
  end

  def finish
    finalize_scalar_token if %i[number literal].include?(@token)
    return unless @valid && @token.nil? && @stack.empty? && @root_state == :done

    @collector.finish
  end

  private

  def consume(byte)
    case @token
    when :string
      consume_string(byte)
      true
    when :number, :literal
      if scalar_token_byte?(byte)
        return invalidate if @token_text.bytesize >= MAX_SCALAR_TOKEN_BYTES

        @token_text << byte
        true
      else
        finalize_scalar_token
        false
      end
    else
      consume_structure(byte)
      true
    end
  end

  def consume_string(byte)
    if @string_unicode
      @string_stats.consume(byte)
      @string_unicode = @string_unicode + 1 == 4 ? nil : @string_unicode + 1
      return
    end
    if @string_escaped
      @string_stats.consume(byte)
      @string_unicode = 0 if byte == 117
      @string_escaped = false
      return
    end
    if byte == 34
      stats = @string_stats.finish
      @token = nil
      if @string_is_key
        key = stats.value
        return invalidate unless stats.valid? && key

        frame = @stack.last
        if @collector.track_object_keys?(frame.path)
          return invalidate if frame.keys.key?(key) || @structural_keys >= MAX_TRACKED_VALUES

          frame.keys[key] = true
          @structural_keys += 1
        end
        frame.key = key
        frame.state = :colon
        @collector.object_key(frame.path, key, stats)
      else
        @collector.string(current_value_path, stats)
        complete_value
      end
    else
      @string_stats.consume(byte)
      @string_escaped = byte == 92
    end
  end

  def consume_structure(byte)
    return if whitespace?(byte)

    frame = @stack.last
    state = frame ? frame.state : @root_state
    case state
    when :value, :value_or_end
      if state == :value_or_end && byte == 93
        close_container(:array)
      else
        start_value(byte)
      end
    when :key_or_end
      if byte == 125
        close_container(:object)
      elsif byte == 34
        start_string(true)
      else
        invalidate
      end
    when :key
      byte == 34 ? start_string(true) : invalidate
    when :colon
      byte == 58 ? frame.state = :value : invalidate
    when :comma_or_end
      if frame.kind == :object
        if byte == 44
          frame.state = :key
        elsif byte == 125
          close_container(:object)
        else
          invalidate
        end
      elsif byte == 44
        frame.state = :value
      elsif byte == 93
        close_container(:array)
      else
        invalidate
      end
    when :done
      invalidate
    else
      invalidate
    end
  end

  def start_value(byte)
    case byte
    when 123
      return invalidate if @stack.length >= MAX_NESTING_DEPTH

      path = current_value_path
      @collector.start_container(path, :object)
      @stack << Frame.new(kind: :object, path: path, state: :key_or_end, keys: {})
    when 91
      return invalidate if @stack.length >= MAX_NESTING_DEPTH

      path = current_value_path
      @collector.start_container(path, :array)
      @stack << Frame.new(kind: :array, path: path, state: :value_or_end, index: 0)
    when 34
      start_string(false)
    when 45, 48..57
      @token = :number
      @token_text = +byte.chr
    when 116, 102, 110
      @token = :literal
      @token_text = +byte.chr
    else
      invalidate
    end
  end

  def start_string(key)
    @token = :string
    @string_is_key = key
    @string_stats = StringStats.new
    @string_escaped = false
    @string_unicode = nil
  end

  def finalize_scalar_token
    text = @token_text
    value = case @token
    when :literal
      { "true" => true, "false" => false, "null" => nil }[text]
    when :number
      if text.match?(/\A-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?\z/)
        text.match?(/[.eE]/) ? Float(text) : Integer(text)
      end
    end
    if value.nil? && text != "null"
      invalidate
    else
      @collector.scalar(current_value_path, value)
      @token = nil
      @token_text = nil
      complete_value
    end
  rescue ArgumentError
    invalidate
  end

  def close_container(kind)
    frame = @stack.last
    return invalidate unless frame&.kind == kind

    @stack.pop
    complete_value
  end

  def complete_value
    frame = @stack.last
    if frame
      if frame.kind == :object
        frame.key = nil
      else
        frame.index += 1
      end
      frame.state = :comma_or_end
    else
      @root_state = :done
    end
  end

  def current_value_path
    frame = @stack.last
    return [] unless frame

    frame.path + [frame.kind == :object ? frame.key : frame.index]
  end

  def scalar_token_byte?(byte)
    byte.between?(48, 57) || [43, 45, 46, 69, 101].include?(byte) || byte.between?(65, 90) || byte.between?(97, 122)
  end

  def whitespace?(byte)
    [9, 10, 13, 32].include?(byte)
  end

  def invalidate
    @valid = false
  end
end
