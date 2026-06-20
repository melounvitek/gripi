require "minitest/autorun"
require_relative "../lib/sessions/session_family"

class SessionsSessionFamilyTest < Minitest::Test
  Session = Struct.new(:path, :parent_session_path, :modified_at, keyword_init: true)

  def test_indexes_parents_children_and_roots_by_normalized_path
    root = session("/tmp/project/root.jsonl", modified_at: Time.at(10))
    older_child = session("/tmp/project/older-child.jsonl", parent: "/tmp/project/../project/root.jsonl", modified_at: Time.at(20))
    newer_child = session("/tmp/project/newer-child.jsonl", parent: "/tmp/project/root.jsonl", modified_at: Time.at(30))
    orphan = session("/tmp/project/orphan.jsonl", parent: "/tmp/project/missing.jsonl", modified_at: Time.at(40))
    family = Sessions::SessionFamily.new([root, older_child, newer_child, orphan])

    assert_equal root, family.parent(older_child)
    assert_equal [newer_child, older_child], family.children(root)
    assert_equal 2, family.child_count(root)
    assert_equal root, family.root(newer_child)
    assert_equal orphan, family.root(orphan)
  end

  def test_stops_root_lookup_at_cycles
    first = session("/tmp/project/first.jsonl", parent: "/tmp/project/second.jsonl")
    second = session("/tmp/project/second.jsonl", parent: "/tmp/project/first.jsonl")
    family = Sessions::SessionFamily.new([first, second])

    assert_equal first, family.root(first)
  end

  private

  def session(path, parent: nil, modified_at: Time.at(0))
    Session.new(path: path, parent_session_path: parent, modified_at: modified_at)
  end
end
