require "rack/protection/host_authorization"

class FriendlyHostAuthorization < Rack::Protection::HostAuthorization
  def accepts?(env)
    return false if options[:deny_all]

    super
  end

  private

  def default_reaction(env)
    deny(env)
  end

  def deny(env)
    request = Rack::Request.new(env)
    authority = rejected_authority(request)
    candidate = options.fetch(:normalize_suggested_host).call(authority)
    lines = [
      "Gripi blocked this hostname.",
      "",
      "Only continue if you recognize this as your intended Gripi address."
    ]

    if candidate
      if options[:configured_hosts_present]
        lines.concat([
          "Append this hostname to the existing GRIPI_PERMITTED_HOSTS value in ~/.config/gripi/env:",
          "",
          candidate
        ])
      else
        lines.concat([
          "Add this line to ~/.config/gripi/env:",
          "",
          "GRIPI_PERMITTED_HOSTS=#{candidate}"
        ])
      end
    else
      lines << "Add the intended exact hostname to GRIPI_PERMITTED_HOSTS in ~/.config/gripi/env."
    end

    if local_https_proxy_request?(env, request)
      lines.concat([
        "",
        "If this request intentionally comes through Tailscale Serve or another trusted HTTPS reverse proxy, also add:",
        "",
        "GRIPI_TRUST_PROXY_HEADERS=1",
        "",
        "Enable proxy trust only when clients cannot bypass a proxy that overwrites forwarded headers."
      ])
    end

    lines.concat([
      "",
      "Then restart Gripi. With the documented systemd service:",
      "",
      "systemctl --user restart gripi.service",
      "",
      "The request remains blocked until configuration and restart are complete."
    ])

    warn env, "attack prevented by #{self.class}"
    [
      options[:status],
      { "content-type" => "text/plain; charset=utf-8", "cache-control" => "no-store", "x-content-type-options" => "nosniff" },
      [lines.join("\n")]
    ]
  end

  def rejected_authority(request)
    origin_host = extract_host(request.host_authority)
    return request.host_authority unless host_permitted?(origin_host)

    request.forwarded_authority || request.host_authority
  end

  def local_https_proxy_request?(env, request)
    return false unless IPAddr.new(env["REMOTE_ADDR"].to_s).loopback?
    return false unless env["HTTP_X_FORWARDED_PROTO"].to_s.split(",").first.to_s.strip.casecmp?("https")

    origin = options.fetch(:normalize_suggested_host).call(request.host_authority)
    forwarded = options.fetch(:normalize_suggested_host).call(request.forwarded_authority)
    origin && origin == forwarded
  rescue IPAddr::InvalidAddressError
    false
  end
end
