package app

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"mfp/internal/config"
	"mfp/internal/server"
	"mfp/internal/state"
)

type App struct {
	apiServer   *http.Server
	adminServer *http.Server
	logger      *log.Logger
}

func New() (*App, error) {
	configPath := os.Getenv("MFP_CONFIG")
	if configPath == "" {
		configPath = "configs/example.json"
	}
	manager := config.NewManager(configPath)
	cfg, err := manager.Load()
	if err != nil {
		return nil, err
	}
	hub, err := state.NewHub(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	logger := log.New(os.Stdout, "[mfp] ", log.LstdFlags|log.Lmicroseconds)
	service := server.New(configPath, cfg, hub, logger)
	return &App{
		apiServer:   service.APIServer(),
		adminServer: service.AdminServer(),
		logger:      logger,
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		a.logger.Printf("api listening on %s", a.apiServer.Addr)
		if err := a.apiServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	go func() {
		defer wg.Done()
		a.logger.Printf("admin listening on %s", a.adminServer.Addr)
		if err := a.adminServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil {
			return err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = a.apiServer.Shutdown(shutdownCtx)
	_ = a.adminServer.Shutdown(shutdownCtx)
	wg.Wait()
	return nil
}
