package server

import "net/http"

const (
	gripiIconGrip = `<path fill="#F24405" d="M0 0H200V100H100V200H0ZM600 0H800V200H700V100H600ZM0 600H100V700H200V800H0ZM700 600H800V800H600V700H700Z"/>`
	gripiIconPI   = `<g fill="#F1EFE9" transform="translate(200 200)"><path fill-rule="evenodd" d="M0 0H300V200H200V300H100V400H0ZM100 100V200H200V100Z"/><path d="M300 200H400V400H300Z"/></g>`
	serviceWorker = `self.addEventListener("install", (event) => {
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
  event.waitUntil(self.registration.showNotification(data.title || "Gripi", {
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
`
)

type manifest struct {
	Name            string         `json:"name"`
	ShortName       string         `json:"short_name"`
	StartURL        string         `json:"start_url"`
	Scope           string         `json:"scope"`
	Display         string         `json:"display"`
	BackgroundColor string         `json:"background_color"`
	ThemeColor      string         `json:"theme_color"`
	Icons           []manifestIcon `json:"icons"`
}

type manifestIcon struct {
	Source  string `json:"src"`
	Sizes   string `json:"sizes"`
	Type    string `json:"type"`
	Purpose string `json:"purpose"`
}

func (app *application) registerPWARoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /manifest.webmanifest", app.webManifest)
	mux.HandleFunc("/manifest.webmanifest", http.NotFound)
	mux.HandleFunc("GET /app-icon.svg", app.appIcon)
	mux.HandleFunc("/app-icon.svg", http.NotFound)
	mux.HandleFunc("GET /app-icon-maskable.svg", app.maskableAppIcon)
	mux.HandleFunc("/app-icon-maskable.svg", http.NotFound)
	mux.HandleFunc("GET /service-worker.js", app.serviceWorker)
	mux.HandleFunc("/service-worker.js", http.NotFound)
	mux.HandleFunc("GET /notification-test", app.notificationTest)
	mux.HandleFunc("/notification-test", http.NotFound)
}

func (app *application) webManifest(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Content-Type", "application/manifest+json")
	writeJSON(response, manifest{
		Name:            "Gripi",
		ShortName:       "Gripi",
		StartURL:        "/",
		Scope:           "/",
		Display:         "standalone",
		BackgroundColor: "#18181e",
		ThemeColor:      "#18181e",
		Icons: []manifestIcon{
			{Source: "/app-icon.svg", Sizes: "any", Type: "image/svg+xml", Purpose: "any"},
			{Source: "/app-icon-maskable.svg", Sizes: "any", Type: "image/svg+xml", Purpose: "maskable"},
		},
	})
}

func (app *application) appIcon(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Content-Type", "image/svg+xml")
	_, _ = response.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 800">
  <rect width="800" height="800" rx="96" fill="#18181e"/>
  <g transform="translate(80 80) scale(0.8)">` + gripiIconGrip + gripiIconPI + `</g>
</svg>
`))
}

func (app *application) maskableAppIcon(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Content-Type", "image/svg+xml")
	_, _ = response.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 800">
  <rect width="800" height="800" fill="#18181e"/>
  <g transform="translate(176 176) scale(0.56)">` + gripiIconGrip + gripiIconPI + `</g>
</svg>
`))
}

func (app *application) serviceWorker(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Content-Type", "application/javascript;charset=utf-8")
	response.Header().Set("Cache-Control", "no-cache")
	_, _ = response.Write([]byte(serviceWorker))
}

func (app *application) notificationTest(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = app.templates.ExecuteTemplate(response, "notification_test.html", struct {
		Nonce string
	}{requestNonce(request)})
}
