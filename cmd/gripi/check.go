package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/melounvitek/gripi/internal/agent/hermes"
	"github.com/melounvitek/gripi/internal/config"
)

func checkPi() error {
	cfg, err := config.Load(os.Environ())
	if err != nil {
		return err
	}
	adapter, err := newPiAdapter(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := adapter.Check(ctx); err != nil || cfg.HermesPath == "" {
		return err
	}
	if err := hermes.Check(ctx, cfg.HermesPath); err != nil {
		return fmt.Errorf("GRIPI_HERMES=%s: %w", cfg.HermesPath, err)
	}
	return nil
}
