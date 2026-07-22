package server

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/melounvitek/gripi/internal/access"
	"github.com/melounvitek/gripi/internal/config"
	"github.com/melounvitek/gripi/internal/rendering"
	"github.com/melounvitek/gripi/internal/rpc"
	"github.com/melounvitek/gripi/internal/sessions"
)

//go:embed templates/*.html
var templateFiles embed.FS

type application struct {
	config             config.Config
	files              fs.FS
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
	rpcClients         *rpc.Registry
	newRPCClient       func(string) (rpc.RPCClient, error)
	rpcDiagnostics     *rpc.Diagnostics
	pendingSessions    *rpc.PendingSessionRegistry
	pendingRemapMu     sync.Mutex
	imagePromptLocks   sync.Map
	synchronizer       *sessions.Synchronizer
	rpcMaintenance     *rpc.Maintenance
	extensionMu        sync.Mutex
	extensionPath      string
	extensionRoot      string
	ownsSession        func(*http.Request, string) bool
	claimSession       func(*http.Request, string) error
	releaseSession     func(*http.Request, string) error
}

type Handler struct {
	next            http.Handler
	app             *application
	closeMu         sync.Mutex
	closed          bool
	maintenanceDone chan struct{}
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	handler.next.ServeHTTP(response, request)
}

func (handler *Handler) Close(ctx context.Context) error {
	handler.closeMu.Lock()
	defer handler.closeMu.Unlock()
	if handler.closed {
		return nil
	}
	if handler.app.rpcMaintenance != nil && handler.maintenanceDone == nil {
		handler.maintenanceDone = make(chan struct{})
		go func() {
			handler.app.rpcMaintenance.Stop()
			close(handler.maintenanceDone)
		}()
	}
	if err := handler.app.rpcClients.Shutdown(ctx); err != nil {
		return err
	}
	if handler.maintenanceDone != nil {
		select {
		case <-handler.maintenanceDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if handler.app.extensionRoot != "" {
		if err := os.RemoveAll(handler.app.extensionRoot); err != nil {
			return err
		}
	}
	handler.closed = true
	return nil
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
		files:              files,
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
		pendingSessions:    rpc.NewPendingSessionRegistry(nil),
	}
	diagnostics := &rpc.Diagnostics{Enabled: cfg.RPCDiagnosticsEnabled, Writer: os.Stderr}
	app.rpcDiagnostics = diagnostics
	app.rpcClients = rpc.NewRegistry(func(sessionPath string) (rpc.RPCClient, error) {
		extensionPath, err := app.rpcExtensionPath()
		if err != nil {
			return nil, err
		}
		return rpc.Start(sessionPath, cfg.PiCommand, extensionPath, diagnostics)
	}, nil)
	app.rpcClients.SetDiagnostics(diagnostics)
	app.newRPCClient = func(cwd string) (rpc.RPCClient, error) {
		extensionPath, err := app.rpcExtensionPath()
		if err != nil {
			return nil, err
		}
		return rpc.StartInCWD(cwd, cfg.PiCommand, extensionPath, diagnostics)
	}
	app.synchronizer = sessions.NewSynchronizer(cfg.SessionsRoot, cfg.Home, app.sessionCache, app.rpcClients)
	if cfg.RPCIdleTimeout > 0 && cfg.RPCIdleSweep > 0 {
		app.rpcMaintenance, _ = rpc.NewMaintenance(cfg.RPCIdleSweep, app.cleanupIdleRPCClients, rpc.LogMaintenanceError(os.Stderr))
		app.rpcMaintenance.Start(context.Background())
	}
	mux := http.NewServeMux()
	assets := filesOnly(public, http.StripPrefix("/", http.FileServerFS(public)))
	mux.Handle("GET /assets/", noCache(assets))
	mux.Handle("GET /apple-touch-icon.png", noCache(assets))
	app.registerBrowserAccessRoutes(mux)
	app.registerPWARoutes(mux)
	app.registerSessionRoutes(mux)
	app.registerActionRoutes(mux)

	var handler http.Handler = mux
	if cfg.MultiUserMode {
		handler = failClosedMultiUserRoutes(handler)
	}
	handler = app.enforceBrowserAccess(handler)
	handler = app.protectUnsafeRequestOrigin(handler)
	handler = app.enforceSecureRemoteTransport(handler)
	handler = app.securityHeaders(handler)
	handler = app.authorizeHost(handler)
	handler = app.limitRequestBody(handler)
	return &Handler{next: handler, app: app}, nil
}

func (app *application) rpcExtensionPath() (string, error) {
	app.extensionMu.Lock()
	defer app.extensionMu.Unlock()
	if app.extensionPath != "" {
		return app.extensionPath, nil
	}
	contents, err := fs.ReadFile(app.files, "pi_extensions/gripi-tree.ts")
	if err != nil {
		return "", fmt.Errorf("read embedded Pi extension: %w", err)
	}
	root, err := os.MkdirTemp("", "gripi-rpc-")
	if err != nil {
		return "", fmt.Errorf("create Pi extension directory: %w", err)
	}
	path := filepath.Join(root, "gripi-tree.ts")
	if err := os.WriteFile(path, contents, 0600); err != nil {
		_ = os.RemoveAll(root)
		return "", fmt.Errorf("write Pi extension: %w", err)
	}
	app.extensionRoot, app.extensionPath = root, path
	return path, nil
}

func (app *application) imagePromptLock(path string) *sync.Mutex {
	lock, _ := app.imagePromptLocks.LoadOrStore(path, &sync.Mutex{})
	return lock.(*sync.Mutex)
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

func failClosedMultiUserRoutes(next http.Handler) http.Handler {
	staticPaths := map[string]bool{
		"/apple-touch-icon.png":  true,
		"/manifest.webmanifest":  true,
		"/app-icon.svg":          true,
		"/app-icon-maskable.svg": true,
		"/service-worker.js":     true,
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/assets/") || staticPaths[request.URL.Path] {
			next.ServeHTTP(response, request)
			return
		}
		writeText(response, http.StatusServiceUnavailable, "Multi-user access is unavailable until ownership support is enabled")
	})
}

func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(response, request)
	})
}
