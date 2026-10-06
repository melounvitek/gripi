package main

import (
	"context"
	"io/fs"
	"os"
	"time"

	gripi "github.com/melounvitek/gripi"
	"github.com/melounvitek/gripi/internal/config"
	"github.com/melounvitek/gripi/internal/rpc"
)

func checkPi() error {
	cfg, err := config.Load(os.Environ())
	if err != nil {
		return err
	}
	extension, err := fs.ReadFile(gripi.WebFiles, "pi_extensions/gripi-tree.ts")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return rpc.CheckExtension(ctx, cfg.PiCommand, extension)
}
