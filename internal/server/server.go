package server

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/melounvitek/gripi/internal/access"
	"github.com/melounvitek/gripi/internal/config"
	"github.com/melounvitek/gripi/internal/keyedlock"
	"github.com/melounvitek/gripi/internal/rendering"
	"github.com/melounvitek/gripi/internal/resource"
	"github.com/melounvitek/gripi/internal/rpc"
	"github.com/melounvitek/gripi/internal/sessions"
	"github.com/melounvitek/gripi/internal/update"
)

//go:embed templates/*.html
var templateFiles embed.FS

type application struct {
	config             config.Config
	files              fs.FS
	templates          *template.Template
	browserStore       *access.BrowserStore
	workspaceStore     *access.WorkspaceStore
	ownershipStore     *access.WorkspaceOwnershipStore
	workspaceSecret    string
	accessLimiter      *access.RateLimiter
	adminLimiter       *access.RateLimiter
	newBrowserToken    func() (string, error)
	instanceID         string
	sessionCache       *sessions.Cache
	gatewayState       *sessions.GatewayState
	markdown           *rendering.Markdown
	heavyRequests      chan struct{}
	fdRequests         chan struct{}
	unknownBodySpools  chan struct{}
	sessionHashesMu    sync.Mutex
	knownSessionHashes map[string]bool
	sessionHashesAt    time.Time
	rpcClients         *rpc.Registry
	newRPCClient       func(string) (rpc.RPCClient, error)
	rpcDiagnostics     *rpc.Diagnostics
	pendingSessions    *rpc.PendingSessionRegistry
	pendingRemapMu     sync.Mutex
	imagePromptLocks   keyedlock.Mutexes
	synchronizer       *sessions.Synchronizer
	rpcMaintenance     *rpc.Maintenance
	resourceMonitor    resourceMonitor
	updateCoordinator  updateCoordinator
	extensionMu        sync.Mutex
	extensionPath      string
	extensionRoot      string
	ownsSession        func(*http.Request, string) bool
	claimSession       func(*http.Request, string) (bool, error)
	releaseSession     func(*http.Request, string) error
}

func logInternalError(operation string, err error) {
	log.Printf("%s: %v", operation, err)
}

func writeInternalError(response http.ResponseWriter, operation string, err error) {
	logInternalError(operation, err)
	writeText(response, http.StatusInternalServerError, "Internal Server Error")
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
	workspaceSecret := ""
	if cfg.MultiUserMode {
		workspaceSecret, err = access.NewWorkspaceSecretStore(cfg.WorkspaceSecretPath).Secret()
		if err != nil {
			return nil, fmt.Errorf("load workspace secret: %w", err)
		}
	}

	app := &application{
		config:             cfg,
		files:              files,
		templates:          templates,
		browserStore:       access.NewBrowserStore(cfg.BrowserAccessPath),
		workspaceStore:     access.NewWorkspaceStore(cfg.WorkspaceAccessPath),
		ownershipStore:     access.NewWorkspaceOwnershipStore(cfg.WorkspaceOwnershipPath),
		workspaceSecret:    workspaceSecret,
		accessLimiter:      access.NewRateLimiter(30, time.Minute),
		adminLimiter:       access.NewRateLimiter(10, 5*time.Minute),
		newBrowserToken:    newBrowserToken,
		instanceID:         instanceID,
		sessionCache:       sessions.NewCache(),
		gatewayState:       sessions.NewGatewayState(cfg.ReadStatePath, cfg.PinnedSessionsPath),
		markdown:           markdown,
		heavyRequests:      make(chan struct{}, 2),
		fdRequests:         make(chan struct{}, 4),
		unknownBodySpools:  make(chan struct{}, unknownBodySpoolLimit),
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
	if cfg.MultiUserMode {
		app.ownsSession = func(request *http.Request, path string) bool {
			owned, err := app.ownershipStore.OwnedBy(path, currentWorkspaceID(request))
			return err == nil && owned
		}
		app.claimSession = func(request *http.Request, path string) (bool, error) {
			return app.ownershipStore.Claim(path, currentWorkspaceID(request))
		}
		app.releaseSession = func(request *http.Request, path string) error {
			return app.ownershipStore.Release(path, currentWorkspaceID(request))
		}
	}
	app.resourceMonitor = resource.NewMonitor()
	directory, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("find gateway working directory: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find gateway executable: %w", err)
	}
	checkout, err := update.DiscoverCheckout(executable, directory, !cfg.Production || flag.Lookup("test.v") != nil)
	if err != nil {
		return nil, fmt.Errorf("find gateway checkout: %w", err)
	}
	updater := update.NewUpdater(checkout)
	updater.AdmitCutover = app.rpcClients.DrainIfIdle
	updater.ResumeCutover = func() { app.rpcClients.ResumeAfterFailedShutdown() }
	app.updateCoordinator = update.NewCoordinator(updater, func() error {
		shutdownSent := false
		err := update.RequestRestart(cfg.RestartPath, app.rpcClients, func() error {
			process, err := os.FindProcess(os.Getpid())
			if err != nil {
				return err
			}
			if err := process.Signal(os.Interrupt); err != nil {
				return err
			}
			shutdownSent = true
			return nil
		})
		if err != nil && !shutdownSent {
			app.rpcClients.ResumeAfterFailedShutdown()
		}
		return err
	}, app.rpcClients.BusySessionCount, app.rpcClients.DrainIfIdle)
	if cfg.RPCIdleTimeout > 0 && cfg.RPCIdleSweep > 0 {
		app.rpcMaintenance, _ = rpc.NewMaintenance(cfg.RPCIdleSweep, app.cleanupIdleRPCClients, rpc.LogMaintenanceError(os.Stderr))
		app.rpcMaintenance.Start(context.Background())
	}
	mux := http.NewServeMux()
	assets := filesOnly(public, http.StripPrefix("/", http.FileServerFS(public)))
	mux.Handle("GET /assets/", noCache(assets))
	mux.Handle("GET /apple-touch-icon.png", noCache(assets))
	app.registerBrowserAccessRoutes(mux)
	app.registerWorkspaceRoutes(mux)
	app.registerPWARoutes(mux)
	app.registerOperationalRoutes(mux)
	app.registerSessionRoutes(mux)
	app.registerActionRoutes(mux)

	var handler http.Handler = mux
	handler = app.enforceWorkspaceAccess(handler)
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
