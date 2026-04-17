package app

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"

	"github.com/chu/3xui-user-sync/internal/config"
	"github.com/chu/3xui-user-sync/internal/security"
	"github.com/chu/3xui-user-sync/internal/store"
	"github.com/chu/3xui-user-sync/internal/xui"
	"github.com/rs/zerolog"
)

type App struct {
	db       *sql.DB
	service  *Service
	http     http.Handler
	shutdown context.CancelFunc
}

func New(ctx context.Context, cfg config.Config, logger zerolog.Logger) (*App, error) {
	runCtx, cancel := context.WithCancel(ctx)

	db, err := store.OpenSQLite(runCtx, cfg.DBPath)
	if err != nil {
		cancel()
		return nil, err
	}

	xuiClient, err := xui.New(cfg.RequestTimeout, logger)
	if err != nil {
		cancel()
		_ = db.Close()
		return nil, err
	}

	svc := NewService(
		cfg,
		logger,
		store.NewUserRepository(db),
		store.NewServerRepository(db),
		security.NewSessionStore(cfg.SessionTTL, cfg.SessionIdleTimeout),
		xuiClient,
	)

	web := NewWeb(cfg, logger, svc)
	return &App{
		db:       db,
		service:  svc,
		http:     web.Router(),
		shutdown: cancel,
	}, nil
}

func (a *App) Router() http.Handler {
	return a.http
}

func (a *App) Close() error {
	if err := a.db.Close(); err != nil {
		return fmt.Errorf("close db: %w", err)
	}
	return nil
}

func (a *App) Cancel() {
	a.shutdown()
}
