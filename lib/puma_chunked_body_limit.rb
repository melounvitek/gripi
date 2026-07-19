require "puma/client"

# Puma 6.6.1 checks chunked size only after the terminal chunk, so enforce its configured limit while decoding.
module PumaChunkedBodyLimit
  private

  def decode_chunk(chunk)
    decoded = super
    return decoded unless above_http_content_limit(@chunked_content_length)

    @http_content_length_limit_exceeded = true
    @env["HTTP_CONNECTION"] = "close"
    @buffer = nil
    @body = Puma::Client::EmptyBody
    set_ready unless decoded
    true
  end
end

Puma::Client.prepend(PumaChunkedBodyLimit)
