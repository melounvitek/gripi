require "uri"

module Web
  module RequestOriginProtection
    UNSAFE_METHODS = %w[POST PUT PATCH DELETE].freeze

    module Helpers
      private

      def protect_unsafe_request_origin!
        return unless unsafe_request?

        halt 403, unsafe_request_rejection_message if cross_site_fetch_request? || !request_origin_allowed?
      end

      def unsafe_request_rejection_message
        if !settings.trust_proxy_headers && !request.env["HTTP_X_FORWARDED_PROTO"].to_s.empty?
          <<~MESSAGE.strip
            Gripi rejected this request because proxy headers are not trusted.

            If this gateway is behind Tailscale Serve or another trusted HTTPS reverse proxy, add this line to ~/.config/gripi/env:

            GRIPI_TRUST_PROXY_HEADERS=1

            Then restart Gripi. With the documented systemd service:

            systemctl --user restart gripi.service

            Enable this only for a trusted proxy that overwrites forwarded headers and cannot be bypassed.
          MESSAGE
        else
          "Cross-origin unsafe request rejected"
        end
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
        origins = [direct_request_origin]
        origins << forwarded_request_origin if settings.trust_proxy_headers
        origins.filter_map { |origin| normalized_request_origin(origin) }.uniq
      end

      def direct_request_origin
        "#{request.env.fetch("rack.url_scheme", "http")}://#{direct_request_authority}"
      end

      def direct_request_authority
        authority = request.env["HTTP_HOST"].to_s
        return authority unless authority.empty?

        host = request.env["SERVER_NAME"].to_s
        host = "[#{host}]" if host.include?(":") && !host.start_with?("[")
        port = request.env["SERVER_PORT"].to_s
        default_port = request.env.fetch("rack.url_scheme", "http") == "https" ? "443" : "80"
        port.empty? || port == default_port ? host : "#{host}:#{port}"
      end

      def forwarded_request_origin
        proto = first_forwarded_value(request.env["HTTP_X_FORWARDED_PROTO"])
        return unless proto

        host = first_forwarded_value(request.env["HTTP_X_FORWARDED_HOST"]) || direct_request_authority
        return if host.to_s.empty?

        port = first_forwarded_value(request.env["HTTP_X_FORWARDED_PORT"])
        host = "#{host}:#{port}" if port&.match?(/\A\d+\z/) && !host.match?(/(?:\]|[^:]):\d+\z/)
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
