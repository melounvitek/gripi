# frozen_string_literal: true

module Rpc
  class TreeProjection
    ENTRY_LIMIT = 1_000
    PREVIEW_TEXT_BYTES = 512
    METADATA_TEXT_BYTES = 1_024
    FILTER_MODES = %w[default no-tools user-only labeled-only all].freeze
    SETTINGS_ENTRY_TYPES = %w[label custom model_change thinking_level_change session_info].freeze

    def self.call(data, filter_mode: "default")
      new.call(data, filter_mode: filter_mode)
    end

    def call(data, filter_mode: "default")
      tree = data.is_a?(Hash) && data["tree"].is_a?(Array) ? data["tree"] : []
      leaf_id = data.is_a?(Hash) ? data["leafId"] : nil
      filter_mode = "default" unless FILTER_MODES.include?(filter_mode)
      visible = []
      total_entries = 0
      current_projection = nil
      latest_projection = nil
      latest_timestamp = nil
      active_subtree_map = active_subtrees(tree, leaf_id)
      stack = active_first(tree, active_subtree_map).reverse_each.map { |node| [node, nil, 0, nil] }

      until stack.empty?
        node, visible_parent_id, visible_depth, visible_ancestor = stack.pop
        next unless node.is_a?(Hash) && node["entry"].is_a?(Hash)

        entry = node["entry"]
        is_visible = visible?(entry, node, leaf_id, filter_mode)
        projection = is_visible ? projected_entry(entry, node, visible_parent_id, visible_depth) : nil
        nearest_visible = projection || visible_ancestor
        current_projection = nearest_visible if entry["id"] == leaf_id
        timestamp = entry["timestamp"].to_s
        if entry["id"] && (latest_timestamp.nil? || timestamp >= latest_timestamp)
          latest_projection = nearest_visible
          latest_timestamp = timestamp
        end

        children = node["children"].is_a?(Array) ? node["children"] : []
        child_parent_id = is_visible ? entry["id"] : visible_parent_id
        child_depth = is_visible ? visible_depth + 1 : visible_depth
        active_first(children, active_subtree_map).reverse_each { |child| stack << [child, child_parent_id, child_depth, nearest_visible] }
        next unless projection

        total_entries += 1
        visible << projection if visible.length < ENTRY_LIMIT
      end

      retain_marked_entries(visible, current_projection, latest_projection)
      current_id = current_projection&.[](:entryId)
      latest_id = latest_projection&.[](:entryId)
      visible.each do |entry|
        entry[:current] = entry[:entryId] == current_id
        entry[:latest] = entry[:entryId] == latest_id
      end
      {
        entries: visible,
        leafId: bounded_metadata(leaf_id),
        truncated: total_entries > ENTRY_LIMIT,
        totalEntries: total_entries
      }
    end

    private

    def active_subtrees(roots, leaf_id)
      nodes = []
      stack = roots.reverse
      until stack.empty?
        node = stack.pop
        next unless node.is_a?(Hash)

        nodes << node
        children = node["children"].is_a?(Array) ? node["children"] : []
        children.reverse_each { |child| stack << child }
      end

      nodes.reverse_each.each_with_object({}) do |node, result|
        children = node["children"].is_a?(Array) ? node["children"] : []
        result[node.object_id] = !leaf_id.nil? && (node.dig("entry", "id") == leaf_id || children.any? { |child| result[child.object_id] })
      end
    end

    def active_first(nodes, active_subtrees)
      active, inactive = nodes.partition { |node| active_subtrees[node.object_id] }
      active + inactive
    end

    def retain_marked_entries(entries, *marked_entries)
      marked_entries = marked_entries.compact.uniq { |entry| entry[:entryId] }
      marked_ids = marked_entries.to_h { |entry| [entry[:entryId], true] }
      missing_entries = marked_entries.reject { |marked| entries.any? { |entry| entry[:entryId] == marked[:entryId] } }
      while entries.length + missing_entries.length > ENTRY_LIMIT
        removable_index = entries.rindex { |entry| !marked_ids[entry[:entryId]] }
        break unless removable_index

        entries.delete_at(removable_index)
      end
      entries.concat(missing_entries.map(&:dup))

      retained_ids = entries.to_h { |entry| [entry[:entryId], true] }
      depths = {}
      entries.each do |entry|
        parent_id = entry[:parentId]
        parent_id = nil unless retained_ids[parent_id] && depths.key?(parent_id)
        entry[:parentId] = parent_id
        entry[:depth] = parent_id ? depths.fetch(parent_id) + 1 : 0
        depths[entry[:entryId]] = entry[:depth]
      end
    end

    def visible?(entry, node, leaf_id, filter_mode)
      if entry["type"] == "message" && entry.dig("message", "role") == "assistant" && entry["id"] != leaf_id
        message = entry["message"]
        has_text = content_text(message["content"]).strip.length.positive?
        unusual_stop = message["stopReason"] && !%w[stop toolUse].include?(message["stopReason"])
        return false unless has_text || unusual_stop
      end

      case filter_mode
      when "user-only"
        entry["type"] == "message" && entry.dig("message", "role") == "user"
      when "no-tools"
        !SETTINGS_ENTRY_TYPES.include?(entry["type"]) && !(entry["type"] == "message" && entry.dig("message", "role") == "toolResult")
      when "labeled-only"
        !node["label"].nil?
      when "all"
        true
      else
        !SETTINGS_ENTRY_TYPES.include?(entry["type"])
      end
    end

    def projected_entry(entry, node, parent_id, depth)
      text = entry_text(entry)
      payload = {
        entryId: bounded_metadata(entry["id"]),
        parentId: bounded_metadata(parent_id),
        depth: depth,
        type: bounded_metadata(entry["type"]),
        role: bounded_metadata(entry_role(entry)),
        text: preview(text),
        timestamp: bounded_metadata(entry["timestamp"]),
        current: false,
        latest: false
      }
      payload[:label] = bounded_text(node["label"], PREVIEW_TEXT_BYTES) unless node["label"].nil?
      payload[:labelTimestamp] = bounded_metadata(node["labelTimestamp"]) unless node["labelTimestamp"].nil?
      payload
    end

    def entry_role(entry)
      case entry["type"]
      when "message" then entry.dig("message", "role") || "message"
      when "custom_message" then "custom"
      when "branch_summary" then "summary"
      when "compaction" then "compact"
      else entry["type"]
      end
    end

    def entry_text(entry)
      case entry["type"]
      when "message" then content_text(entry.dig("message", "content"))
      when "custom_message" then content_text(entry["content"])
      when "branch_summary", "compaction" then entry["summary"].to_s
      else ""
      end
    end

    def content_text(content)
      return content if content.is_a?(String)
      return "" unless content.is_a?(Array)

      content.filter_map do |part|
        next part if part.is_a?(String)
        part["text"] if part.is_a?(Hash) && part["type"] == "text"
      end.join
    end

    def preview(text)
      normalized = text.to_s.gsub(/\s+/, " ").strip
      bounded_text(normalized, PREVIEW_TEXT_BYTES)
    end

    def bounded_metadata(value)
      value.nil? ? nil : bounded_text(value, METADATA_TEXT_BYTES)
    end

    def bounded_text(value, byte_limit)
      text = value.to_s
      return text if text.bytesize <= byte_limit

      omission = "…"
      prefix = text.byteslice(0, byte_limit - omission.bytesize)
      prefix = prefix.byteslice(0, prefix.bytesize - 1) until prefix.valid_encoding?
      "#{prefix}#{omission}"
    end
  end
end
