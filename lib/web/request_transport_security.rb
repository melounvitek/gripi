require "ipaddr"

module Web
  module RequestTransportSecurity
    module Helpers
      private

      def secure_transport?
        forwarded_scheme = trusted_forwarded_scheme
        return forwarded_scheme.casecmp?("https") unless forwarded_scheme.empty?

        request.env.fetch("rack.url_scheme", "http") == "https"
      end

      def trusted_forwarded_scheme
        return "" unless settings.trust_proxy_headers

        request.env["HTTP_X_FORWARDED_PROTO"].to_s.split(",").first.to_s.strip
      end

      def loopback_client?
        IPAddr.new(request.env["REMOTE_ADDR"].to_s).loopback?
      rescue IPAddr::InvalidAddressError
        false
      end

      def enforce_secure_remote_transport!
        return unless settings.enforce_secure_remote_transport
        return if secure_transport?
        return if loopback_client? && trusted_forwarded_scheme.empty?

        halt 403, "Remote Gripi access requires HTTPS. See docs/configuration.md."
      end
    end

    def self.registered(app)
      app.helpers Helpers

      app.before do
        enforce_secure_remote_transport!
      end
    end
  end
end
