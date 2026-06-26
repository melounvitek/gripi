require "base64"
require "digest"
require "fileutils"
require "json"
require "time"
require "pathname"
require "rack/utils"

class PiAttachmentStore
  MATCH_WINDOW_SECONDS = 5 * 60

  ALLOWED_IMAGE_MIME_TYPES = {
    "image/png" => "png",
    "image/jpeg" => "jpg",
    "image/gif" => "gif",
    "image/webp" => "webp"
  }.freeze

  Attachment = Struct.new(:message_hash, :timestamp, :count, :paths, :mime_types, keyword_init: true)

  def initialize(root: File.expand_path("~/.pi/web-gateway/attachments"))
    @root = root
  end

  def persist_prompt_images(session_path, images)
    session_dir = attachment_dir(session_path)
    FileUtils.mkdir_p(session_dir)

    images.filter_map do |image|
      mime_type = image[:mimeType] || image["mimeType"]
      extension = ALLOWED_IMAGE_MIME_TYPES[mime_type]
      next unless extension

      data = Base64.strict_decode64((image[:data] || image["data"]).to_s)
      path = File.join(session_dir, "#{Digest::SHA256.hexdigest(data)}.#{extension}")
      File.binwrite(path, data) unless File.exist?(path)
      path
    rescue ArgumentError
      nil
    end
  end

  def record_prompt(session_path, message, image_count, timestamp: Time.now, paths: [], mime_types: [])
    return unless image_count.positive?

    FileUtils.mkdir_p(@root)
    File.open(metadata_path(session_path), "a") do |file|
      file.flock(File::LOCK_EX)
      file.puts(JSON.generate(
        "message_hash" => message_hash(message),
        "timestamp" => timestamp.utc.iso8601(6),
        "count" => image_count,
        "paths" => paths,
        "mime_types" => mime_types
      ))
    end
  end

  def migrate_session(from_session_path, to_session_path)
    return if from_session_path.to_s == to_session_path.to_s

    from_path = metadata_path(from_session_path)
    return unless File.exist?(from_path)

    FileUtils.mkdir_p(@root)
    File.open(metadata_path(to_session_path), "a") do |to_file|
      to_file.flock(File::LOCK_EX)
      File.readlines(from_path, chomp: true).each { |line| to_file.puts(line) }
    end
  end

  def counts_for_messages(session_path, messages)
    matched_attachments_for_messages(session_path, messages).transform_values(&:count)
  end

  def images_for_messages(session_path, messages)
    matched_attachments_for_messages(session_path, messages).each_with_object({}) do |(message_id, attachment), images|
      paths = Array(attachment.paths)
      next if paths.empty?

      images[message_id] = paths.map.with_index do |path, index|
        {
          path: path,
          mime_type: attachment.mime_types[index],
          src: attachment_url(path)
        }
      end
    end
  end

  private

  def matched_attachments_for_messages(session_path, messages)
    attachments = read_attachments(session_path)
    return {} if attachments.empty?

    used = {}
    messages.each_with_object({}) do |message, matched|
      next unless message.role == "user"

      index = best_attachment_index(attachments, used, message)
      next unless index

      used[index] = true
      matched[message.object_id] = attachments[index]
    end
  end

  def best_attachment_index(attachments, used, message)
    hash = message_hash(message.text)
    candidates = attachments.each_with_index.select do |attachment, index|
      !used[index] && attachment.message_hash == hash
    end
    return nil if candidates.empty?

    return candidates.first.last unless message.timestamp

    timed_candidates = candidates.filter_map do |attachment, index|
      next unless attachment.timestamp

      distance = (attachment.timestamp - message.timestamp).abs
      [distance, index] if distance <= MATCH_WINDOW_SECONDS
    end
    timed_candidates.min_by(&:first)&.last
  end

  def read_attachments(session_path)
    path = metadata_path(session_path)
    return [] unless File.exist?(path)

    File.readlines(path, chomp: true).filter_map do |line|
      entry = JSON.parse(line)
      Attachment.new(
        message_hash: entry["message_hash"],
        timestamp: parse_time(entry["timestamp"]),
        count: entry["count"].to_i,
        paths: Array(entry["paths"]),
        mime_types: Array(entry["mime_types"])
      )
    rescue JSON::ParserError
      nil
    end
  end

  def metadata_path(session_path)
    File.join(@root, "#{session_hash(session_path)}.jsonl")
  end

  def attachment_dir(session_path)
    File.join(@root, session_hash(session_path))
  end

  def session_hash(session_path)
    Digest::SHA256.hexdigest(session_path)
  end

  def attachment_url(path)
    relative = Pathname.new(path).relative_path_from(Pathname.new(@root)).to_s
    "/attachments/#{relative.split(File::SEPARATOR).map { |part| Rack::Utils.escape_path(part) }.join("/")}"
  rescue ArgumentError
    nil
  end

  def message_hash(message)
    Digest::SHA256.hexdigest(normalized_message_text(message))
  end

  def normalized_message_text(text)
    text.to_s.gsub(/\r\n?/, "\n").strip
  end

  def parse_time(value)
    Time.parse(value) if value
  rescue ArgumentError, TypeError
    nil
  end
end
