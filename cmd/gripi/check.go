package main

import (
	"context"
	"os"
	"time"

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
	return adapter.Check(ctx)
}
