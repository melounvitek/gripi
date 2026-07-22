package server

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/melounvitek/gripi/internal/access"
	"github.com/melounvitek/gripi/internal/config"
	"github.com/melounvitek/gripi/internal/rendering"
	"github.com/melounvitek/gripi/internal/sessions"
)

//go:embed templates/*.html
var templateFiles embed.FS

type application struct {
	config             config.Config
	templates          *template.Template
	browserStore       *access.BrowserStore
	accessLimiter      *access.RateLimiter
	adminLimiter       *access.RateLimiter
	newBrowserToken    func() (string, error)
	instanceID         string
	sessionCache       *sessions.Cache
	gatewayState       *sessions.GatewayState
	markdown           *rendering.Markdown
	heavyRequests      chan struct{}
	fdRequests         chan struct{}
	sessionHashesMu    sync.Mutex
	knownSessionHashes map[string]bool
	sessionHashesAt    time.Time
}

func NewHandler(cfg config.Config, files fs.FS) (http.Handler, error) {
	return newHandler(cfg, files, randomBrowserToken)
}

func newHandler(cfg config.Config, files fs.FS, newBrowserToken func() (string, error)) (http.Handler, error) {
	public, err := fs.Sub(files, "public")
	if err != nil {
		return nil, fmt.Errorf("open embedded public files: %w", err)
	}
	markdown := rendering.NewMarkdown()
	templates, err := template.New("").Funcs(templateFunctions(markdown)).ParseFS(templateFiles, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	instanceID, err := randomBrowserToken()
	if err != nil {
		return nil, fmt.Errorf("generate gateway instance ID: %w", err)
	}

	app := &application{
		config:             cfg,
		templates:          templates,
		browserStore:       access.NewBrowserStore(cfg.BrowserAccessPath),
		accessLimiter:      access.NewRateLimiter(30, time.Minute),
		adminLimiter:       access.NewRateLimiter(10, 5*time.Minute),
		newBrowserToken:    newBrowserToken,
		instanceID:         instanceID,
		sessionCache:       sessions.NewCache(),
		gatewayState:       sessions.NewGatewayState(cfg.ReadStatePath, cfg.PinnedSessionsPath),
		markdown:           markdown,
		heavyRequests:      make(chan struct{}, 2),
		fdRequests:         make(chan struct{}, 4),
		knownSessionHashes: make(map[string]bool),
	}
	mux := http.NewServeMux()
	assets := filesOnly(public, http.StripPrefix("/", http.FileServerFS(public)))
	mux.Handle("GET /assets/", noCache(assets))
	mux.Handle("GET /apple-touch-icon.png", noCache(assets))
	app.registerBrowserAccessRoutes(mux)
	app.registerPWARoutes(mux)
	app.registerSessionRoutes(mux)

	var handler http.Handler = mux
	handler = app.enforceBrowserAccess(handler)
	handler = app.protectUnsafeRequestOrigin(handler)
	handler = app.enforceSecureRemoteTransport(handler)
	handler = app.securityHeaders(handler)
	handler = app.authorizeHost(handler)
	handler = app.limitRequestBody(handler)
	return handler, nil
}

func filesOnly(root fs.FS, next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		file, err := fs.Stat(root, strings.TrimPrefix(request.URL.Path, "/"))
		if err != nil || file.IsDir() {
			http.NotFound(response, request)
			return
		}
		next.ServeHTTP(response, request)
	})
}

func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(response, request)
	})
}
