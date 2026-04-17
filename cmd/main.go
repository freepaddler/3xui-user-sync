package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chu/3xui-user-sync/internal/app"
	"github.com/chu/3xui-user-sync/internal/config"
	"github.com/chu/3xui-user-sync/internal/logging"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}

	logger := logging.New(cfg)
	ctx := logger.WithContext(context.Background())

	application, err := app.New(ctx, cfg, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("bootstrap failed")
	}
	defer func() {
		if closeErr := application.Close(); closeErr != nil {
			logger.Error().Err(closeErr).Msg("shutdown cleanup failed")
		}
	}()

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           application.Router(),
		BaseContext: func(_ net.Listener) context.Context {
			return ctx
		},
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		logger.Info().Str("addr", cfg.HTTPAddr).Msg("http server starting")
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatal().Err(err).Msg("http server failed")
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	application.Cancel()

	shutdownErr := make(chan error, 1)
	go func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		shutdownErr <- server.Shutdown(shutdownCtx)
	}()

	select {
	case err := <-shutdownErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error().Err(err).Msg("http shutdown failed")
		}
	case <-time.After(20 * time.Second):
		if err := server.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error().Err(err).Msg("http force close failed")
		}
	}
}
