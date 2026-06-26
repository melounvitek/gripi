require "base64"
require "stringio"
require "tempfile"
require "minitest/autorun"
require_relative "../lib/prompts/uploaded_images"

class PromptsUploadedImagesTest < Minitest::Test
  def test_encodes_uploaded_images
    upload = { tempfile: StringIO.new("fake image data"), type: "image/png" }

    images = Prompts::UploadedImages.parse([upload])

    assert_equal [
      { type: "image", data: Base64.strict_encode64("fake image data"), mimeType: "image/png" }
    ], images
  end

  def test_accepts_rack_uploaded_file_shape
    file = Tempfile.new("screenshot")
    file.binmode
    file.write("fake image data")
    file.rewind
    upload = Object.new
    upload.define_singleton_method(:tempfile) { file }
    upload.define_singleton_method(:content_type) { "image/png" }

    images = Prompts::UploadedImages.parse(upload)

    assert_equal Base64.strict_encode64("fake image data"), images.first.fetch(:data)
  ensure
    file&.close!
  end

  def test_rejects_too_many_images
    error = assert_raises(Prompts::UploadedImages::ValidationError) do
      Prompts::UploadedImages.parse(Array.new(6) { { tempfile: StringIO.new("image"), type: "image/png" } })
    end

    assert_equal "Too many images", error.message
  end

  def test_rejects_unsupported_image_uploads
    error = assert_raises(Prompts::UploadedImages::ValidationError) do
      Prompts::UploadedImages.parse([{ tempfile: StringIO.new("svg"), type: "image/svg+xml" }])
    end

    assert_equal "Only image uploads are supported", error.message
  end

  def test_rejects_non_image_uploads
    error = assert_raises(Prompts::UploadedImages::ValidationError) do
      Prompts::UploadedImages.parse([{ tempfile: StringIO.new("text"), type: "text/plain" }])
    end

    assert_equal "Only image uploads are supported", error.message
  end

  def test_rejects_large_uploads
    upload = { tempfile: StringIO.new("x" * (Prompts::UploadedImages::MAX_IMAGE_BYTES + 1)), type: "image/png" }

    error = assert_raises(Prompts::UploadedImages::ValidationError) do
      Prompts::UploadedImages.parse([upload])
    end

    assert_equal "Image upload is too large", error.message
  end
end
