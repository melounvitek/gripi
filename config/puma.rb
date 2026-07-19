require_relative "../lib/puma_chunked_body_limit"

http_content_length_limit 64 * 1024 * 1024
