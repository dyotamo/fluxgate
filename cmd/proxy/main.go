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

	"github.com/dyotamo/fluxgate/internal/config"
	"github.com/dyotamo/fluxgate/internal/router"
)

func main() {
	configPath := flag.String("config", "configs/proxy.yaml", "path to the proxy YAML configuration")
	flag.Parse()

	if err := run(*configPath); err != nil {
		slog.Error("fluxgate stopped", "error", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	routeHandler, err := router.New(cfg.Routes, cfg.Proxy.Timeout.Value(), logger)
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:         cfg.Server.Listen,
		Handler:      routeHandler,
		ReadTimeout:  cfg.Server.ReadTimeout.Value(),
		WriteTimeout: cfg.Server.WriteTimeout.Value(),
		IdleTimeout:  cfg.Server.IdleTimeout.Value(),
		ErrorLog:     slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("starting Fluxgate", "listen", server.Addr, "routes", len(cfg.Routes))
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-signalContext.Done():
		logger.Info("shutdown signal received")
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout.Value())
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		logger.Error("graceful shutdown failed; forcing server close", "error", err)
		if closeErr := server.Close(); closeErr != nil {
			return closeErr
		}
	}

	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-time.After(cfg.Server.ShutdownTimeout.Value()):
		return errors.New("server did not stop before shutdown timeout")
	}
	return nil
}
