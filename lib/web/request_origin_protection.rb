require "uri"

module Web
  module RequestOriginProtection
    UNSAFE_METHODS = %w[POST PUT PATCH DELETE].freeze

    module Helpers
      private

      def protect_unsafe_request_origin!
        return unless unsafe_request?

        halt 403, "Cross-origin unsafe request rejected" if cross_site_fetch_request? || !request_origin_allowed?
      end

      def unsafe_request?
        RequestOriginProtection::UNSAFE_METHODS.include?(request.request_method)
      end

      def cross_site_fetch_request?
        request.env["HTTP_SEC_FETCH_SITE"].to_s.downcase == "cross-site"
      end

      def request_origin_allowed?
        origin = request.env["HTTP_ORIGIN"].to_s.strip
        return origin_allowed?(origin) unless origin.empty?

        referer = request.env["HTTP_REFERER"].to_s.strip
        return true if referer.empty?

        origin_allowed?(referer)
      end

      def origin_allowed?(origin)
        normalized_origin = normalized_request_origin(origin)
        normalized_origin && allowed_request_origins.include?(normalized_origin)
      end

      def allowed_request_origins
        origins = [request.base_url]
        forwarded_origin = forwarded_request_origin
        origins << forwarded_origin if forwarded_origin
        origins.filter_map { |origin| normalized_request_origin(origin) }.uniq
      end

      def forwarded_request_origin
        proto = first_forwarded_value(request.env["HTTP_X_FORWARDED_PROTO"])
        return unless proto

        host = first_forwarded_value(request.env["HTTP_X_FORWARDED_HOST"]) || request.host_with_port
        return if host.to_s.empty?

        port = first_forwarded_value(request.env["HTTP_X_FORWARDED_PORT"])
        host = "#{host}:#{port}" if port && !host.include?(":")
        "#{proto}://#{host}"
      end

      def first_forwarded_value(value)
        value.to_s.split(",").first&.strip&.then { |part| part unless part.empty? }
      end

      def normalized_request_origin(origin)
        uri = URI.parse(origin)
        return unless %w[http https].include?(uri.scheme)
        return if uri.host.to_s.empty?

        [uri.scheme, uri.host.downcase, uri.port]
      rescue URI::InvalidURIError
        nil
      end
    end

    def self.registered(app)
      app.helpers Helpers

      app.before do
        protect_unsafe_request_origin!
      end
    end
  end
end
