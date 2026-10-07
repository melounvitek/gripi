package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"embed"
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/melounvitek/gripi/internal/access"
	"github.com/melounvitek/gripi/internal/config"
	"github.com/melounvitek/gripi/internal/environment"
	"github.com/melounvitek/gripi/internal/keyedlock"
	"github.com/melounvitek/gripi/internal/push"
	"github.com/melounvitek/gripi/internal/rendering"
	"github.com/melounvitek/gripi/internal/resource"
	"github.com/melounvitek/gripi/internal/rpc"
	"github.com/melounvitek/gripi/internal/sessions"
	"github.com/melounvitek/gripi/internal/update"
)

//go:embed templates/*.html
var templateFiles embed.FS

type application struct {
	config                  config.Config
	files                   fs.FS
	templates               *template.Template
	browserStore            *access.BrowserStore
	workspaceStore          *access.WorkspaceStore
	ownershipStore          *access.WorkspaceOwnershipStore
	environment             *environment.Store
	workspaceSecret         string
	pushIdentity            *push.VAPIDIdentity
	pushSubscriptions       *push.SubscriptionStore
	pushNotifier            pushNotifier
	notificationPresence    *notificationPresence
	accessLimiter           *access.RateLimiter
	adminLimiter            *access.RateLimiter
	newBrowserToken         func() (string, error)
	instanceID              string
	sessionCache            *sessions.Cache
	gatewayState            *sessions.GatewayState
	markdown                *rendering.Markdown
	heavyRequests           chan struct{}
	imageRequests           chan struct{}
	fdRequests              chan struct{}
	unknownBodySpools       chan struct{}
	sessionHashesMu         sync.Mutex
	knownSessionHashes      map[string]bool
	sessionHashesAt         time.Time
	rpcClients              *rpc.Registry
	newRPCClient            func(cwd, userID string) (rpc.RPCClient, error)
	rpcDiagnostics          *rpc.Diagnostics
	pendingSessions         *rpc.PendingSessionRegistry
	pendingRemapMu          sync.Mutex
	imagePromptLocks        keyedlock.Mutexes
	promptAdmissions        sessionAdmissions
	sessionMutationLocks    keyedlock.Mutexes
	synchronizer            *sessions.Synchronizer
	rpcMaintenance          *rpc.Maintenance
	completionNotifications *completionNotifier
	resourceMonitor         resourceMonitor
	updateCoordinator       updateCoordinator
	extensionMu             sync.Mutex
	extensionPath           string
	extensionRoot           string
	ownsSession             func(*http.Request, string) bool
	claimSession            func(*http.Request, string) (bool, error)
	releaseSession          func(*http.Request, string) error
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
	local           http.Handler
	app             *application
	closeMu         sync.Mutex
	closed          bool
	maintenanceDone chan struct{}
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	handler.next.ServeHTTP(response, request)
}

// Local serves gripi commands. It skips browser authentication, so it must only
// be exposed on a socket that the gateway's own user can open. It is nil in
// multi-user mode, where no single user owns every session.
func (handler *Handler) Local() http.Handler {
	return handler.local
}

func (handler *Handler) Close(ctx context.Context) error {
	handler.closeMu.Lock()
	defer handler.closeMu.Unlock()
	if handler.closed {
		return nil
	}
	if handler.app.sessionCache != nil {
		if err := handler.app.sessionCache.Save(); err != nil {
			logInternalError("save session metadata cache", err)
		}
	}
	if handler.app.updateCoordinator != nil {
		if err := handler.app.updateCoordinator.Close(ctx); err != nil {
			return err
		}
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
	if handler.app.completionNotifications != nil {
		if err := handler.app.completionNotifications.Close(ctx); err != nil {
			return err
		}
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
	if handler.app.synchronizer != nil {
		if err := handler.app.synchronizer.SaveBaselines(); err != nil {
			logInternalError("save session baselines", err)
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

	pushIdentity := push.NewVAPIDIdentity(cfg.WebPushVAPIDPath)
	pushSubscriptions := push.NewSubscriptionStore(cfg.PushSubscriptionsPath)
	pushDelivery := push.NewWebPushDelivery(pushIdentity, "https://github.com/melounvitek/gripi", &http.Client{Timeout: 15 * time.Second})

	app := &application{
		config:               cfg,
		files:                files,
		templates:            templates,
		browserStore:         access.NewBrowserStore(cfg.BrowserAccessPath),
		workspaceStore:       access.NewWorkspaceStore(cfg.WorkspaceAccessPath),
		ownershipStore:       access.NewWorkspaceOwnershipStore(cfg.WorkspaceOwnershipPath, cfg.SessionsRoot),
		environment:          environment.NewStore(cfg.EnvironmentPath),
		workspaceSecret:      workspaceSecret,
		pushIdentity:         pushIdentity,
		pushSubscriptions:    pushSubscriptions,
		pushNotifier:         push.NewNotifier(pushSubscriptions, pushDelivery),
		notificationPresence: newNotificationPresence(time.Now),
		accessLimiter:        access.NewRateLimiter(30, time.Minute),
		adminLimiter:         access.NewRateLimiter(10, 5*time.Minute),
		newBrowserToken:      newBrowserToken,
		instanceID:           instanceID,
		sessionCache:         sessions.NewCache(),
		gatewayState:         sessions.NewGatewayState(cfg.ReadStatePath, cfg.PinnedSessionsPath, cfg.SessionTagsPath, cfg.SessionsRoot),
		markdown:             markdown,
		heavyRequests:        make(chan struct{}, 2),
		imageRequests:        make(chan struct{}, 2),
		fdRequests:           make(chan struct{}, 4),
		unknownBodySpools:    make(chan struct{}, unknownBodySpoolLimit),
		knownSessionHashes:   make(map[string]bool),
		pendingSessions:      rpc.NewPendingSessionRegistry(nil),
	}
	if cfg.ReadStatePath != "" {
		app.sessionCache = sessions.LoadCache(filepath.Join(filepath.Dir(cfg.ReadStatePath), "session-metadata-cache.json"))
		store := sessions.Store{Root: cfg.SessionsRoot, Home: cfg.Home, Cache: app.sessionCache}
		existing, err := store.Sessions()
		if err != nil {
			return nil, fmt.Errorf("list existing projects: %w", err)
		}
		if err := app.sessionCache.Save(); err != nil {
			logInternalError("save session metadata cache", err)
		}
		if _, err := app.gatewayState.ProjectCWDs(existing); err != nil {
			return nil, err
		}
	}
	diagnostics := &rpc.Diagnostics{Enabled: cfg.RPCDiagnosticsEnabled, Writer: os.Stderr}
	app.rpcDiagnostics = diagnostics
	app.completionNotifications = newCompletionNotifier(app)
	app.rpcClients = rpc.NewRegistry(func(sessionPath string) (rpc.RPCClient, error) {
		owner := ""
		if cfg.MultiUserMode {
			var err error
			if owner, err = app.ownershipStore.Owner(sessionPath); err != nil {
				return nil, err
			}
		}
		variables, err := app.piEnvironment(owner)
		if err != nil {
			return nil, err
		}
		extensionPath, err := app.rpcExtensionPath()
		if err != nil {
			return nil, err
		}
		return rpc.Start(sessionPath, cfg.PiCommand, extensionPath, variables, diagnostics, app.completionNotifications.Observe)
	}, nil)
	app.rpcClients.SetDiagnostics(diagnostics)
	app.newRPCClient = func(cwd, userID string) (rpc.RPCClient, error) {
		variables, err := app.piEnvironment(userID)
		if err != nil {
			return nil, err
		}
		extensionPath, err := app.rpcExtensionPath()
		if err != nil {
			return nil, err
		}
		return rpc.StartInCWD(cwd, cfg.PiCommand, extensionPath, variables, diagnostics, app.completionNotifications.Observe)
	}
	app.synchronizer = sessions.NewSynchronizer(cfg.SessionsRoot, cfg.Home, app.sessionCache, app.rpcClients)
	if cfg.ReadStatePath != "" {
		state := filepath.Dir(cfg.ReadStatePath)
		app.synchronizer.RestoreBaselines(filepath.Join(state, "session-baselines.json"))
		if err := app.synchronizer.PersistExternalFollow(filepath.Join(state, "external-sessions.json")); err != nil {
			return nil, err
		}
	}
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
	app.updateCoordinator = update.NewCoordinator(updater, app.requestGatewayRestart, app.rpcClients.BusySessionCount, app.rpcClients.DrainIfIdle)
	if cfg.RPCIdleTimeout > 0 && cfg.RPCIdleSweep > 0 {
		app.rpcMaintenance, _ = rpc.NewMaintenance(cfg.RPCIdleSweep, app.cleanupIdleRPCClients, rpc.LogMaintenanceError(os.Stderr))
		app.rpcMaintenance.Start(context.Background())
	}
	mux := http.NewServeMux()
	assets := filesOnly(public, gzipText(public, http.StripPrefix("/", http.FileServerFS(public))))
	mux.Handle("GET /assets/", noStore(assets))
	mux.Handle("GET /apple-touch-icon.png", noStore(assets))
	app.registerBrowserAccessRoutes(mux)
	app.registerWorkspaceRoutes(mux)
	app.registerPWARoutes(mux)
	app.registerPushRoutes(mux)
	app.registerOperationalRoutes(mux)
	app.registerSessionRoutes(mux)
	app.registerActionRoutes(mux)
	app.registerEnvironmentRoutes(mux)

	var handler http.Handler = mux
	handler = app.enforceWorkspaceAccess(handler)
	handler = app.enforceBrowserAccess(handler)
	handler = app.protectUnsafeRequestOrigin(handler)
	handler = app.enforceSecureRemoteTransport(handler)
	handler = app.securityHeaders(handler)
	handler = app.authorizeHost(handler)
	handler = app.limitRequestBody(handler)
	gateway := &Handler{next: handler, app: app}
	if !cfg.MultiUserMode {
		local := http.NewServeMux()
		app.registerLocalRoutes(local)
		gateway.local = app.limitRequestBody(local)
	}
	return gateway, nil
}

type restartRegistry interface {
	Shutdown(context.Context) error
	ResumeAfterFailedShutdown() bool
}

func (app *application) requestGatewayRestart(ctx context.Context) error {
	return requestGatewayRestart(ctx, app.config.RestartPath, app.rpcClients, func() error {
		process, err := os.FindProcess(os.Getpid())
		if err != nil {
			return err
		}
		return process.Signal(os.Interrupt)
	})
}

func requestGatewayRestart(ctx context.Context, path string, registry restartRegistry, shutdown func() error) error {
	shutdownSent := false
	err := update.RequestRestart(ctx, path, registry, func() error {
		if err := shutdown(); err != nil {
			return err
		}
		shutdownSent = true
		return nil
	})
	if err != nil && !shutdownSent && ctx.Err() == nil {
		registry.ResumeAfterFailedShutdown()
	}
	return err
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

// Packed, scripts and styles are about a quarter of the size. They are chosen by extension, not by MIME type, because the type of .js depends on the host's mime.types.
// A request with Range is left to the file server, so its offsets still count bytes of the unpacked file.
func gzipText(root fs.FS, next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		extension := filepath.Ext(request.URL.Path)
		if (extension != ".js" && extension != ".mjs" && extension != ".css") || request.Header.Get("Range") != "" || !strings.Contains(request.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(response, request)
			return
		}
		contents, err := fs.ReadFile(root, strings.TrimPrefix(request.URL.Path, "/"))
		if err != nil {
			next.ServeHTTP(response, request)
			return
		}
		var packed bytes.Buffer
		writer := gzip.NewWriter(&packed)
		_, _ = writer.Write(contents)
		_ = writer.Close()
		response.Header().Set("Content-Type", mime.TypeByExtension(extension))
		response.Header().Set("Content-Encoding", "gzip")
		response.Header().Set("Vary", "Accept-Encoding")
		response.Header().Set("Content-Length", strconv.Itoa(packed.Len()))
		_, _ = response.Write(packed.Bytes())
	})
}

// Imported modules have unversioned URLs, and a browser restoring a page from history reuses cached copies without
// revalidating them, so after a gateway update it would run old modules unless they are never stored.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(response, request)
	})
}
