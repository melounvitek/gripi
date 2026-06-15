require "digest"
require "fileutils"
require "json"
require "time"

class PiAttachmentStore
  MATCH_WINDOW_SECONDS = 5 * 60

  Attachment = Struct.new(:message_hash, :timestamp, :count, keyword_init: true)

  def initialize(root: File.expand_path("~/.pi/web-gateway/attachments"))
    @root = root
  end

  def record_prompt(session_path, message, image_count, timestamp: Time.now)
    return unless image_count.positive?

    FileUtils.mkdir_p(@root)
    File.open(metadata_path(session_path), "a") do |file|
      file.flock(File::LOCK_EX)
      file.puts(JSON.generate(
        "message_hash" => message_hash(message),
        "timestamp" => timestamp.utc.iso8601(6),
        "count" => image_count
      ))
    end
  end

  def counts_for_messages(session_path, messages)
    attachments = read_attachments(session_path)
    return {} if attachments.empty?

    used = {}
    messages.each_with_object({}) do |message, counts|
      next unless message.role == "user"

      index = best_attachment_index(attachments, used, message)
      next unless index

      used[index] = true
      counts[message.object_id] = attachments[index].count
    end
  end

  private

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
        count: entry["count"].to_i
      )
    rescue JSON::ParserError
      nil
    end
  end

  def metadata_path(session_path)
    File.join(@root, "#{Digest::SHA256.hexdigest(session_path)}.jsonl")
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
