require "base64"
require "json"
require "minitest/autorun"
require "tmpdir"
require_relative "../lib/pi_attachment_store"
require_relative "../lib/pi_session_store"

class PiAttachmentStoreTest < Minitest::Test
  def test_migrates_pending_session_metadata_to_real_session_key_without_moving_files
    Dir.mktmpdir do |dir|
      pending_path = File.join(dir, "pending.jsonl")
      real_path = File.join(dir, "real.jsonl")
      store = PiAttachmentStore.new(root: File.join(dir, "attachments"))
      image_data = "fake image data"
      paths = store.persist_prompt_images(
        pending_path,
        [{ type: "image", data: Base64.strict_encode64(image_data), mimeType: "image/png" }]
      )
      store.record_prompt(pending_path, "Look\n\n#{paths.first}", 1, paths: paths, mime_types: ["image/png"])
      message = PiSessionStore::Message.new(role: "user", text: "Look\n\n#{paths.first}")

      store.migrate_session(pending_path, real_path)

      assert_equal image_data, File.binread(paths.first)
      assert_equal 1, store.counts_for_messages(real_path, [message]).fetch(message.object_id)
      assert_equal paths.first, store.images_for_messages(real_path, [message]).fetch(message.object_id).first.fetch(:path)
    end
  end

  def test_persists_uploaded_images_to_stable_session_files
    Dir.mktmpdir do |dir|
      session_path = File.join(dir, "sessions", "session.jsonl")
      image_data = "fake image data"
      store = PiAttachmentStore.new(root: File.join(dir, "attachments"))

      paths = store.persist_prompt_images(
        session_path,
        [{ type: "image", data: Base64.strict_encode64(image_data), mimeType: "image/png" }]
      )

      assert_equal 1, paths.length
      assert_equal image_data, File.binread(paths.first)
      assert_match %r{/attachments/[a-f0-9]{64}/[a-f0-9]{64}\.png\z}, paths.first
    end
  end
end
