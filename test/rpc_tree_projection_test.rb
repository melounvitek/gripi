# frozen_string_literal: true

require "minitest/autorun"
require "json"
require_relative "../lib/rpc/tree_projection"

module Rpc
  class TreeProjectionTest < Minitest::Test
    def test_projects_native_tree_without_exposing_images_or_unbounded_text
      long_text = "x" * 20_000
      tree = [
        node(
          entry("user-1", nil, "message", message: { "role" => "user", "content" => [
            { "type" => "text", "text" => long_text },
            { "type" => "image", "data" => "raw-image", "mimeType" => "image/png" }
          ] }),
          label: "checkpoint",
          label_timestamp: "2026-06-13T10:00:01Z",
          children: [node(entry("assistant-1", "user-1", "message", message: { "role" => "assistant", "content" => [{ "type" => "text", "text" => "Answer" }] }))]
        )
      ]

      result = TreeProjection.call({ "tree" => tree, "leafId" => "assistant-1" }, filter_mode: "default")

      assert_equal ["user-1", "assistant-1"], result.fetch(:entries).map { |item| item.fetch(:entryId) }
      user = result.fetch(:entries).first
      assert_equal "checkpoint", user.fetch(:label)
      assert_equal "2026-06-13T10:00:01Z", user.fetch(:labelTimestamp)
      assert_operator user.fetch(:text).bytesize, :<=, TreeProjection::PREVIEW_TEXT_BYTES
      refute user.key?(:editorText)
      refute_includes JSON.generate(result), "raw-image"
      assert_equal "assistant-1", result.fetch(:leafId)
      assert result.fetch(:entries).last.fetch(:current)
    end

    def test_normalizes_preview_text_without_projecting_editor_text
      tree = [
        node(entry("user-1", nil, "message", message: {
          "role" => "user",
          "content" => [{ "type" => "text", "text" => "  First" }, { "type" => "image", "data" => "raw-image" }, { "type" => "text", "text" => " second  " }]
        }))
      ]

      result = TreeProjection.call({ "tree" => tree, "leafId" => "user-1" })
      projected = result.fetch(:entries).first

      refute projected.key?(:editorText)
      assert_equal "First second", projected.fetch(:text)
      refute_includes JSON.generate(projected), "raw-image"
    end

    def test_applies_native_filter_modes_and_keeps_projection_bounded
      children = [
        node(entry("tool-1", "user-1", "message", message: { "role" => "toolResult", "content" => [{ "type" => "text", "text" => "output" }] })),
        node(entry("label-entry", "user-1", "label")),
        node(entry("labeled-assistant", "user-1", "message", message: { "role" => "assistant", "content" => [{ "type" => "text", "text" => "Labeled" }] }), label: "keep")
      ]
      tree = [node(entry("user-1", nil, "message", message: { "role" => "user", "content" => "Prompt" }), children: children)]

      no_tools = TreeProjection.call({ "tree" => tree, "leafId" => "user-1" }, filter_mode: "no-tools")
      labeled = TreeProjection.call({ "tree" => tree, "leafId" => "user-1" }, filter_mode: "labeled-only")

      assert_equal ["user-1", "labeled-assistant"], no_tools.fetch(:entries).map { |item| item.fetch(:entryId) }
      assert_equal ["labeled-assistant"], labeled.fetch(:entries).map { |item| item.fetch(:entryId) }
    end

    def test_reattaches_visible_descendants_to_the_nearest_visible_ancestor
      assistant = node(entry("assistant-1", "label-1", "message", message: { "role" => "assistant", "content" => "Answer" }))
      label = node(entry("label-1", "user-1", "label"), children: [assistant])
      tree = [node(entry("user-1", nil, "message", message: { "role" => "user", "content" => "Prompt" }), children: [label])]

      result = TreeProjection.call({ "tree" => tree, "leafId" => "assistant-1" })

      entries = result.fetch(:entries)
      assert_equal [nil, "user-1"], entries.map { |item| item.fetch(:parentId) }
      assert_equal [0, 1], entries.map { |item| item.fetch(:depth) }
    end

    def test_resolves_hidden_current_and_latest_entries_to_their_visible_ancestor
      label = node(entry("label-1", "assistant-1", "label", timestamp: "2026-06-13T10:02:00Z"))
      assistant = node(
        entry("assistant-1", "user-1", "message", timestamp: "2026-06-13T10:01:00Z", message: { "role" => "assistant", "content" => "Answer" }),
        children: [label]
      )
      tree = [node(entry("user-1", nil, "message", message: { "role" => "user", "content" => "Prompt" }), children: [assistant])]

      result = TreeProjection.call({ "tree" => tree, "leafId" => "label-1" })

      assert_equal [false, true], result.fetch(:entries).map { |item| item.fetch(:current) }
      assert_equal [false, true], result.fetch(:entries).map { |item| item.fetch(:latest) }
      assert_equal "label-1", result.fetch(:leafId)
    end

    def test_marks_the_most_recent_appended_entry_as_latest_across_branches
      older_branch = node(
        entry("user-older", "root", "message", timestamp: "2026-06-13T10:01:00Z", message: { "role" => "user", "content" => "Older branch" }),
        children: [node(entry("assistant-newest", "user-older", "message", timestamp: "2026-06-13T10:04:00Z", message: { "role" => "assistant", "content" => "Newest entry" }))]
      )
      newer_sibling = node(entry("user-middle", "root", "message", timestamp: "2026-06-13T10:02:00Z", message: { "role" => "user", "content" => "Middle entry" }))
      tree = [node(entry("root", nil, "message", timestamp: "2026-06-13T10:00:00Z", message: { "role" => "user", "content" => "Root" }), children: [older_branch, newer_sibling])]

      result = TreeProjection.call({ "tree" => tree, "leafId" => "root" })

      latest = result.fetch(:entries).select { |item| item.fetch(:latest) }
      assert_equal ["assistant-newest"], latest.map { |item| item.fetch(:entryId) }
    end

    def test_prioritizes_the_active_branch_before_applying_the_entry_limit
      inactive_root = node(entry("inactive-0", "root", "message", message: { "role" => "user", "content" => "Inactive 0" }))
      inactive_parent = inactive_root
      1.upto(TreeProjection::ENTRY_LIMIT) do |index|
        child = node(entry("inactive-#{index}", "inactive-#{index - 1}", "message", message: { "role" => "user", "content" => "Inactive #{index}" }))
        inactive_parent.fetch("children") << child
        inactive_parent = child
      end
      active_leaf = node(entry("active-2", "active-1", "message", message: { "role" => "assistant", "content" => "Active answer" }))
      active_root = node(entry("active-1", "root", "message", message: { "role" => "user", "content" => "Active prompt" }), children: [active_leaf])
      tree = [node(entry("root", nil, "message", message: { "role" => "user", "content" => "Root" }), children: [inactive_root, active_root])]

      result = TreeProjection.call({ "tree" => tree, "leafId" => "active-2" }, filter_mode: "all")
      entries = result.fetch(:entries)

      assert_equal ["root", "active-1", "active-2"], entries.first(3).map { |item| item.fetch(:entryId) }
      assert_equal [nil, "root", "active-1"], entries.first(3).map { |item| item.fetch(:parentId) }
      assert entries.fetch(2).fetch(:current)
      assert result.fetch(:truncated)
    end

    def test_retains_distinct_current_and_latest_entries_when_both_exceed_the_limit
      active_root = node(entry("active-0", "root", "message", message: { "role" => "user", "content" => "Active 0" }))
      active_parent = active_root
      1.upto(TreeProjection::ENTRY_LIMIT) do |index|
        child = node(entry("active-#{index}", "active-#{index - 1}", "message", timestamp: "2026-06-13T10:00:00Z", message: { "role" => "user", "content" => "Active #{index}" }))
        active_parent.fetch("children") << child
        active_parent = child
      end
      latest = node(entry("latest", "root", "message", timestamp: "2026-06-13T11:00:00Z", message: { "role" => "assistant", "content" => "Latest" }))
      tree = [node(entry("root", nil, "message", timestamp: "2026-06-13T09:00:00Z", message: { "role" => "user", "content" => "Root" }), children: [active_root, latest])]

      result = TreeProjection.call({ "tree" => tree, "leafId" => "active-#{TreeProjection::ENTRY_LIMIT}" }, filter_mode: "all")
      entries = result.fetch(:entries)

      assert_equal TreeProjection::ENTRY_LIMIT, entries.length
      assert_equal ["active-#{TreeProjection::ENTRY_LIMIT}"], entries.select { |item| item.fetch(:current) }.map { |item| item.fetch(:entryId) }
      assert_equal ["latest"], entries.select { |item| item.fetch(:latest) }.map { |item| item.fetch(:entryId) }
    end

    def test_bounds_all_strings_copied_from_native_tree_metadata
      long = "x" * 2_000
      child = node(entry("child", long, "message", timestamp: long, message: { "role" => long, "content" => "" }), label: long, label_timestamp: long)
      tree = [node(entry(long, nil, long, timestamp: long), children: [child])]

      result = TreeProjection.call({ "tree" => tree, "leafId" => long }, filter_mode: "all")
      root, projected_child = result.fetch(:entries)

      assert_operator result.fetch(:leafId).bytesize, :<=, TreeProjection::METADATA_TEXT_BYTES
      assert_operator root.fetch(:entryId).bytesize, :<=, TreeProjection::METADATA_TEXT_BYTES
      assert_operator root.fetch(:type).bytesize, :<=, TreeProjection::METADATA_TEXT_BYTES
      assert_operator root.fetch(:role).bytesize, :<=, TreeProjection::METADATA_TEXT_BYTES
      assert_operator root.fetch(:timestamp).bytesize, :<=, TreeProjection::METADATA_TEXT_BYTES
      assert_operator projected_child.fetch(:parentId).bytesize, :<=, TreeProjection::METADATA_TEXT_BYTES
      assert_operator projected_child.fetch(:labelTimestamp).bytesize, :<=, TreeProjection::METADATA_TEXT_BYTES
    end

    def test_limits_projected_entries_and_reports_truncation
      root = node(entry("user-0", nil, "message", message: { "role" => "user", "content" => "Prompt 0" }))
      parent = root
      1.upto(TreeProjection::ENTRY_LIMIT) do |index|
        child = node(entry("user-#{index}", "user-#{index - 1}", "message", message: { "role" => "user", "content" => "Prompt #{index}" }))
        parent.fetch("children") << child
        parent = child
      end

      result = TreeProjection.call({ "tree" => [root], "leafId" => "user-#{TreeProjection::ENTRY_LIMIT}" }, filter_mode: "all")

      entries = result.fetch(:entries)
      assert_equal TreeProjection::ENTRY_LIMIT, entries.length
      assert result.fetch(:truncated)
      assert_equal TreeProjection::ENTRY_LIMIT + 1, result.fetch(:totalEntries)
      assert_equal ["user-#{TreeProjection::ENTRY_LIMIT}"], entries.select { |entry| entry.fetch(:current) }.map { |entry| entry.fetch(:entryId) }
      assert_equal ["user-#{TreeProjection::ENTRY_LIMIT}"], entries.select { |entry| entry.fetch(:latest) }.map { |entry| entry.fetch(:entryId) }
      assert_nil entries.last.fetch(:parentId)
      assert_equal 0, entries.last.fetch(:depth)
    end

    private

    def entry(id, parent_id, type, message: nil, timestamp: "2026-06-13T10:00:00Z")
      value = { "id" => id, "parentId" => parent_id, "type" => type, "timestamp" => timestamp }
      value["message"] = message if message
      value
    end

    def node(entry, children: [], label: nil, label_timestamp: nil)
      value = { "entry" => entry, "children" => children }
      value["label"] = label if label
      value["labelTimestamp"] = label_timestamp if label_timestamp
      value
    end
  end
end
