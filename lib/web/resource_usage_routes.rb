require "json"

module Web
  module ResourceUsageRoutes
    def self.registered(app)
      app.get "/resource-usage" do
        not_found unless settings.resource_monitoring_enabled

        headers "Cache-Control" => "no-store"
        content_type :json
        snapshot = settings.resource_usage_monitor.snapshot
        next JSON.generate(supported: false) unless snapshot

        JSON.generate(
          supported: true,
          memoryBytes: snapshot.fetch(:memory_bytes),
          workingSetBytes: snapshot.fetch(:working_set_bytes),
          inactiveFileBytes: snapshot.fetch(:inactive_file_bytes),
          cpuUsageUsec: snapshot.fetch(:cpu_usage_usec),
          pumaRssBytes: snapshot.fetch(:puma_rss_bytes),
          piRssBytes: snapshot.fetch(:pi_rss_bytes),
          piProcessCount: snapshot.fetch(:pi_process_count)
        )
      end
    end
  end
end
