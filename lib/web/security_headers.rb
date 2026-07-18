module Web
  module SecurityHeaders
    module Helpers
      private

      def csp_nonce
        @csp_nonce ||= SecureRandom.base64(24)
      end

      def apply_security_headers!
        policy = [
          "default-src 'self'",
          "script-src 'self' 'nonce-#{csp_nonce}'",
          "style-src 'self' 'unsafe-inline'",
          "img-src 'self' data: blob:",
          "font-src 'self'",
          "connect-src 'self'",
          "worker-src 'self'",
          "manifest-src 'self'",
          "object-src 'none'",
          "base-uri 'none'",
          "frame-ancestors 'none'",
          "form-action 'self'"
        ].join("; ")
        headers(
          "Content-Security-Policy" => policy,
          "Referrer-Policy" => "no-referrer",
          "X-Content-Type-Options" => "nosniff"
        )
        headers "Strict-Transport-Security" => "max-age=31536000" if secure_transport?
      end
    end

    def self.registered(app)
      app.helpers Helpers

      app.before do
        apply_security_headers!
      end
    end
  end
end
