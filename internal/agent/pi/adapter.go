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
	"github.com/melounvitek/gripi/internal/agent/pi/rpc"
	"github.com/melounvitek/gripi/internal/agent/pi/sessionfile"
	"github.com/melounvitek/gripi/internal/agent/pi/settings"
)

type Options struct {
	// Command runs Pi; the adapter appends its RPC arguments.
	Command             []string
	AgentDir            string
	AutoApproveProjects bool
	// Extension is the gateway's Pi extension, which carries the requests Pi's RPC mode lacks.
	Extension []byte
	// WorkerExtension is the extension that only Pi on a worker loads, which holds results until main has the changes.
	WorkerExtension []byte
	// SessionsRoot is where Pi saves sessions, and Home the folder that session paths are shown relative to.
	SessionsRoot string
	Home         string
	// StateDir keeps what the adapter learns about sessions across restarts. Without it the adapter forgets.
	StateDir string
	// Launcher may run a session's Pi elsewhere, such as on a worker.
	Launcher agent.Launcher
}

type Adapter struct {
	command         []string
	launcher        agent.Launcher
	extension       []byte
	workerExtension []byte
	settings        settings.SettingsResolver
	extensionMu     sync.Mutex
	extensionRoot   string
	stateDir        string
	store           sessionStore
	synchronizer    *sessionfile.Synchronizer
}

var (
	_ agent.Adapter = (*Adapter)(nil)
	_ agent.Guard   = (*sessionfile.Synchronizer)(nil)
)

func New(options Options) *Adapter {
	cache := sessionfile.NewCache()
	if options.StateDir != "" {
		cache = sessionfile.LoadCache(filepath.Join(options.StateDir, "session-metadata-cache.json"))
	}
	return &Adapter{
		command:         options.Command,
		launcher:        options.Launcher,
		extension:       options.Extension,
		workerExtension: options.WorkerExtension,
		settings:        settings.SettingsResolver{AgentDir: options.AgentDir, AutoApproveProjects: options.AutoApproveProjects},
		stateDir:        options.StateDir,
		store:           sessionStore{sessionfile.Store{Root: options.SessionsRoot, Home: options.Home, Cache: cache}},
	}
}

func (adapter *Adapter) Store() agent.Store {
	return adapter.store
}

// Guard keeps the gateway's Pi and Pi CLI from both writing a session file. It restores what the previous run
// knew about the files, so it must be called only once.
func (adapter *Adapter) Guard(clients *agent.Registry) (agent.Guard, error) {
	synchronizer := sessionfile.NewSynchronizer(clients)
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
	extensionPath, _, err := adapter.ExtensionFiles()
	if err != nil {
		return nil, err
	}
	return start(options.Observe, func(observer func(*rpc.Client, map[string]any)) (*rpc.Client, error) {
		return rpc.Start(key, options.CWD, adapter.command, extensionPath, options.Env, adapter.launcher, options.Diagnostics, observer)
	})
}

func (adapter *Adapter) StartInCWD(cwd, forkFrom string, options agent.StartOptions) (agent.Client, error) {
	extensionPath, _, err := adapter.ExtensionFiles()
	if err != nil {
		return nil, err
	}
	return start(options.Observe, func(observer func(*rpc.Client, map[string]any)) (*rpc.Client, error) {
		return rpc.StartInCWD(cwd, forkFrom, adapter.command, extensionPath, options.Env, adapter.launcher, options.Diagnostics, observer)
	})
}

func (adapter *Adapter) Composer(string) agent.Composer {
	return Composer{}
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

// ExtensionFiles returns the extracted extensions: the gateway's, which every Pi loads, and the worker extension,
// which a session's Pi on a worker loads with a second --extension. They share a folder, which a worker gets a copy
// of at the same path, so with a state folder it is the same folder in it after every restart.
func (adapter *Adapter) ExtensionFiles() (tree, worker string, err error) {
	adapter.extensionMu.Lock()
	defer adapter.extensionMu.Unlock()
	if adapter.extensionRoot == "" {
		root := filepath.Join(adapter.stateDir, "pi-extensions")
		if adapter.stateDir == "" {
			root, err = os.MkdirTemp("", "gripi-rpc-")
		} else {
			err = os.MkdirAll(root, 0700)
		}
		if err != nil {
			return "", "", fmt.Errorf("create Pi extension directory: %w", err)
		}
		for name, contents := range map[string][]byte{"gripi-tree.ts": adapter.extension, "gripi-worker.ts": adapter.workerExtension} {
			if err := os.WriteFile(filepath.Join(root, name), contents, 0600); err != nil {
				_ = os.RemoveAll(root)
				return "", "", fmt.Errorf("write Pi extension: %w", err)
			}
		}
		adapter.extensionRoot = root
	}
	return filepath.Join(adapter.extensionRoot, "gripi-tree.ts"), filepath.Join(adapter.extensionRoot, "gripi-worker.ts"), nil
}
