require "minitest/autorun"
require "rack/mock"
require "ipaddr"
require_relative "../lib/friendly_host_authorization"

class FriendlyHostAuthorizationTest < Minitest::Test
  def setup
    normalizer = lambda do |authority|
      value = authority.to_s.sub(/:\d+\z/, "").downcase
      value if value.match?(/\A(?:[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?|\d+(?:\.\d+){3})\z/)
    end
    @app = FriendlyHostAuthorization.new(
      ->(_env) { [200, { "content-type" => "text/plain" }, ["ok"]] },
      permitted_hosts: ["127.0.0.1"],
      deny_all: false,
      normalize_suggested_host: normalizer,
      configured_hosts_present: false,
      logging: false
    )
    @request = Rack::MockRequest.new(@app)
  end

  def test_recommends_the_forwarded_host_when_that_authority_was_rejected
    response = @request.get(
      "/",
      "HTTP_HOST" => "127.0.0.1",
      "HTTP_X_FORWARDED_HOST" => "gateway.example.ts.net"
    )

    assert_equal 403, response.status
    assert_includes response.body, "GRIPI_PERMITTED_HOSTS=gateway.example.ts.net"
    refute_includes response.body, "GRIPI_PERMITTED_HOSTS=127.0.0.1"
  end

  def test_rfc_forwarded_authority_does_not_produce_the_wrong_origin_suggestion
    response = @request.get(
      "/",
      "HTTP_HOST" => "127.0.0.1",
      "HTTP_FORWARDED" => "host=gateway.example.ts.net;proto=https"
    )

    assert_equal 403, response.status
    assert_includes response.body, "gateway.example.ts.net"
    refute_includes response.body, "GRIPI_PERMITTED_HOSTS=127.0.0.1"
  end

  def test_leading_dot_host_is_not_suggested_as_an_exact_permission
    response = @request.get("/", "HTTP_HOST" => ".example")

    assert_equal 403, response.status
    assert_includes response.body, "Add the intended exact hostname"
    refute_includes response.body, "GRIPI_PERMITTED_HOSTS=.example"
  end

  def test_deny_all_rejects_even_a_host_in_the_permitted_list
    app = FriendlyHostAuthorization.new(
      ->(_env) { [200, {}, ["ok"]] },
      permitted_hosts: ["127.0.0.1"],
      deny_all: true,
      normalize_suggested_host: ->(_authority) { "127.0.0.1" },
      configured_hosts_present: false,
      logging: false
    )

    response = Rack::MockRequest.new(app).get("/", "HTTP_HOST" => "127.0.0.1")

    assert_equal 403, response.status
  end
end
