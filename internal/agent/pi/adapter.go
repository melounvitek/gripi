// Package pi runs the Pi coding agent CLI behind the agent contract.
package pi

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/melounvitek/gripi/internal/agent"
	pisettings "github.com/melounvitek/gripi/internal/pi"
	"github.com/melounvitek/gripi/internal/rpc"
	"github.com/melounvitek/gripi/internal/sessions"
)

type Options struct {
	// Command runs Pi; the adapter appends its RPC arguments.
	Command             []string
	AgentDir            string
	AutoApproveProjects bool
	// Extension is the gateway's Pi extension, which carries the requests Pi's RPC mode lacks.
	Extension []byte
	// SessionsRoot is where Pi saves sessions, and Home the folder that session paths are shown relative to.
	SessionsRoot string
	Home         string
	// StateDir keeps what the adapter learns about sessions across restarts. Without it the adapter forgets.
	StateDir string
}

type Adapter struct {
	command       []string
	extension     []byte
	settings      pisettings.SettingsResolver
	extensionMu   sync.Mutex
	extensionPath string
	extensionRoot string
	stateDir      string
	store         sessionStore
	synchronizer  *sessions.Synchronizer
}

var (
	_ agent.Adapter = (*Adapter)(nil)
	_ agent.Guard   = (*sessions.Synchronizer)(nil)
)

func New(options Options) *Adapter {
	cache := sessions.NewCache()
	if options.StateDir != "" {
		cache = sessions.LoadCache(filepath.Join(options.StateDir, "session-metadata-cache.json"))
	}
	return &Adapter{
		command:   options.Command,
		extension: options.Extension,
		settings:  pisettings.SettingsResolver{AgentDir: options.AgentDir, AutoApproveProjects: options.AutoApproveProjects},
		stateDir:  options.StateDir,
		store:     sessionStore{sessions.Store{Root: options.SessionsRoot, Home: options.Home, Cache: cache}},
	}
}

func (adapter *Adapter) Store() agent.Store {
	return adapter.store
}

// Guard keeps the gateway's Pi and Pi CLI from both writing a session file. It restores what the previous run
// knew about the files, so it must be called only once.
func (adapter *Adapter) Guard(clients *agent.Registry) (agent.Guard, error) {
	synchronizer := sessions.NewSynchronizer(adapter.store.Root, adapter.store.Home, adapter.store.Cache, clients)
	if adapter.stateDir != "" {
		synchronizer.RestoreBaselines(filepath.Join(adapter.stateDir, "session-baselines.json"))
		if err := synchronizer.PersistExternalFollow(filepath.Join(adapter.stateDir, "external-sessions.json")); err != nil {
			return nil, err
		}
	}
	adapter.synchronizer = synchronizer
	return synchronizer, nil
}

func (adapter *Adapter) Start(key string, options agent.StartOptions) (agent.Client, error) {
	extensionPath, err := adapter.extensionFile()
	if err != nil {
		return nil, err
	}
	client, err := rpc.Start(key, adapter.command, extensionPath, options.Env, options.Diagnostics, observer(options.Observe))
	if err != nil {
		return nil, err
	}
	return client, nil
}

func (adapter *Adapter) StartInCWD(cwd, forkFrom string, options agent.StartOptions) (agent.Client, error) {
	extensionPath, err := adapter.extensionFile()
	if err != nil {
		return nil, err
	}
	client, err := rpc.StartInCWD(cwd, forkFrom, adapter.command, extensionPath, options.Env, options.Diagnostics, observer(options.Observe))
	if err != nil {
		return nil, err
	}
	return client, nil
}

func observer(observe func(agent.Client, map[string]any)) func(*rpc.Client, map[string]any) {
	if observe == nil {
		return nil
	}
	return func(client *rpc.Client, event map[string]any) { observe(client, event) }
}

func (adapter *Adapter) DisplaySettings(cwd string) agent.DisplaySettings {
	settings := adapter.settings.DisplaySettings(cwd)
	return agent.DisplaySettings{HideThinkingBlock: settings.HideThinkingBlock}
}

// Close removes the extracted extension and saves the baselines of the session files, so it must run after every
// Pi process has stopped: the gateway's own last writes would otherwise read as Pi CLI's after a restart.
func (adapter *Adapter) Close() error {
	if err := adapter.removeExtension(); err != nil {
		return err
	}
	if adapter.synchronizer != nil {
		if err := adapter.synchronizer.SaveBaselines(); err != nil {
			log.Printf("save session baselines: %v", err)
		}
	}
	return nil
}

func (adapter *Adapter) removeExtension() error {
	adapter.extensionMu.Lock()
	defer adapter.extensionMu.Unlock()
	if adapter.extensionRoot == "" {
		return nil
	}
	return os.RemoveAll(adapter.extensionRoot)
}

// Check starts Pi with the extension and reports why the commands that carry the gateway's requests are missing.
func (adapter *Adapter) Check(ctx context.Context) error {
	return rpc.CheckExtension(ctx, adapter.command, adapter.extension)
}

func (adapter *Adapter) extensionFile() (string, error) {
	adapter.extensionMu.Lock()
	defer adapter.extensionMu.Unlock()
	if adapter.extensionPath != "" {
		return adapter.extensionPath, nil
	}
	root, err := os.MkdirTemp("", "gripi-rpc-")
	if err != nil {
		return "", fmt.Errorf("create Pi extension directory: %w", err)
	}
	path := filepath.Join(root, "gripi-tree.ts")
	if err := os.WriteFile(path, adapter.extension, 0600); err != nil {
		_ = os.RemoveAll(root)
		return "", fmt.Errorf("write Pi extension: %w", err)
	}
	adapter.extensionRoot, adapter.extensionPath = root, path
	return path, nil
}
