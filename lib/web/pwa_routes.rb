require "json"

module Web
  module PwaRoutes
    def self.registered(app)
      app.get "/manifest.webmanifest" do
        content_type "application/manifest+json"
        JSON.generate(
          name: "GRIPi",
          short_name: "GRIPi",
          start_url: "/",
          scope: "/",
          display: "standalone",
          background_color: "#18181e",
          theme_color: "#18181e",
          icons: [
            { src: "/app-icon.svg", sizes: "any", type: "image/svg+xml", purpose: "any maskable" }
          ]
        )
      end

      app.get "/app-icon.svg" do
        content_type "image/svg+xml"
        <<~SVG
          <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512">
            <rect width="512" height="512" rx="112" fill="#18181e"/>
            <circle cx="256" cy="256" r="168" fill="#282832" stroke="#8abeb7" stroke-width="24"/>
            <text x="256" y="296" text-anchor="middle" font-family="system-ui, -apple-system, sans-serif" font-size="148" font-weight="800" fill="#8abeb7">π</text>
          </svg>
        SVG
      end

      app.get "/service-worker.js" do
        content_type "application/javascript"
        headers "Cache-Control" => "no-cache"
        <<~JS
          self.addEventListener("install", (event) => {
            self.skipWaiting();
          });

          self.addEventListener("activate", (event) => {
            event.waitUntil(self.clients.claim());
          });

          self.addEventListener("message", (event) => {
            const data = event.data || {};
            if (!["gripi-notification", "gripi-notification-test"].includes(data.type)) return;

            const defaultUrl = data.type === "gripi-notification-test" ? "/notification-test" : "/";
            const defaultTag = data.type === "gripi-notification-test" ? "gripi-notification-test" : "gripi-notification";
            event.waitUntil(self.registration.showNotification(data.title || "GRIPi", {
              body: data.body || "Notifications are working.",
              tag: data.tag || defaultTag,
              renotify: true,
              icon: "/app-icon.svg",
              badge: "/app-icon.svg",
              data: { url: data.url || defaultUrl }
            }));
          });

          self.addEventListener("notificationclick", (event) => {
            event.notification.close();
            const url = event.notification.data?.url || "/";
            event.waitUntil((async () => {
              const clientList = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
              for (const client of clientList) {
                if ("focus" in client) {
                  await client.focus();
                  if ("navigate" in client) await client.navigate(url);
                  return;
                }
              }
              if (self.clients.openWindow) await self.clients.openWindow(url);
            })());
          });
        JS
      end

      app.get "/notification-test" do
        erb :notification_test
      end
    end
  end
end
