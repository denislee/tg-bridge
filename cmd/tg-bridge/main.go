package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"tg-bridge/internal/bridge"
	"tg-bridge/internal/config"
	"tg-bridge/internal/httpapi"
	"tg-bridge/internal/tlsutil"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}

	logLevel := slog.LevelInfo
	_ = logLevel.UnmarshalText([]byte(cfg.LogLevel))
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(log)

	cert, fp, err := tlsutil.EnsureSelfSigned(cfg.TLS.CertPath, cfg.TLS.KeyPath, cfg.TLS.Hosts)
	if err != nil {
		log.Error("tls", "err", err)
		os.Exit(1)
	}
	log.Info("tls ready", "sha256", fp)

	br, err := bridge.New(cfg, log)
	if err != nil {
		log.Error("bridge init", "err", err)
		os.Exit(1)
	}
	defer br.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	srv := httpapi.New(cfg, log, br, cert)

	errCh := make(chan error, 2)
	go func() {
		if err := br.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			errCh <- err
		}
	}()
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-errCh:
		log.Error("fatal", "err", err)
	}

	shutdownCtx, shCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shCancel()
	_ = srv.Shutdown(shutdownCtx)
}
