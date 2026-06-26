require "base64"

module Prompts
  class UploadedImages
    MAX_IMAGES = 5
    MAX_IMAGE_BYTES = 10 * 1024 * 1024
    ALLOWED_MIME_TYPES = %w[image/png image/jpeg image/gif image/webp].freeze

    ValidationError = Class.new(StandardError)

    def self.parse(upload_param)
      new(upload_param).images
    end

    def initialize(upload_param)
      @uploads = Array(upload_param).compact
    end

    def images
      raise ValidationError, "Too many images" if uploads.length > MAX_IMAGES

      uploads.map { |upload| image_from(upload) }
    end

    private

    attr_reader :uploads

    def image_from(upload)
      tempfile = uploaded_tempfile(upload)
      mime_type = uploaded_content_type(upload).to_s
      raise ValidationError, "Only image uploads are supported" unless tempfile && ALLOWED_MIME_TYPES.include?(mime_type)
      raise ValidationError, "Image upload is too large" if tempfile.size > MAX_IMAGE_BYTES

      tempfile.rewind if tempfile.respond_to?(:rewind)
      { type: "image", data: Base64.strict_encode64(tempfile.read), mimeType: mime_type }
    end

    def uploaded_tempfile(upload)
      return upload.tempfile if upload.respond_to?(:tempfile)
      return File.open(upload.path, "rb") if upload.respond_to?(:path)
      return upload[:tempfile] if upload.is_a?(Hash) && upload.key?(:tempfile)

      upload["tempfile"] if upload.is_a?(Hash)
    end

    def uploaded_content_type(upload)
      return upload.content_type if upload.respond_to?(:content_type)
      return upload[:type] if upload.is_a?(Hash) && upload.key?(:type)

      upload["type"] if upload.is_a?(Hash)
    end
  end
end
