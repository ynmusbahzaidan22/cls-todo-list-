package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/admin"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/auth"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/config"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/database"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/health"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/httpapi"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/repository"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/task"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		client := http.Client{Timeout: 2 * time.Second}
		response, err := client.Get("http://127.0.0.1:8080/health/ready")
		if err != nil {
			os.Exit(1)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	pool, err := database.Open(context.Background(), cfg.DatabaseURL, cfg.DatabaseMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()
	if cfg.AppEnv == "production" {
		verifyContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := database.VerifyRuntimeRole(verifyContext, pool, cfg.DatabaseRuntimeRole); err != nil {
			return err
		}
	}

	authService, err := auth.NewManager(repository.NewAuthRepository(pool))
	if err != nil {
		return err
	}
	cookieName := "todo_session"
	if cfg.SessionCookieSecure {
		cookieName = "__Host-todo_session"
	}
	authAPI := httpapi.NewAuthHandlers(authService, cookieName, cfg.SessionCookieSecure)
	taskService := task.NewService(repository.NewTaskRepository(pool), cfg.TaskCursorSigningKey)
	taskAPI := httpapi.NewTaskHandlers(taskService)
	templateService := task.NewTemplateService(repository.NewTemplateRepository(pool))
	templateAPI := httpapi.NewTemplateHandlers(templateService)
	preferencesAPI := httpapi.NewPreferenceHandlers(repository.NewPreferenceRepository(pool))
	adminService := admin.NewService(repository.NewAdminRepository(pool), cfg.TaskCursorSigningKey)
	adminAPI := httpapi.NewAdminHandlers(adminService)

	server := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: httpapi.NewRouter(health.NewService(pool), logger, ".", httpapi.RouterOptions{
			Auth:                 authAPI,
			Tasks:                taskAPI,
			Admin:                adminAPI,
			Templates:            templateAPI,
			Preferences:          preferencesAPI,
			TrustedOrigin:        cfg.TrustedOrigin,
			TrustedProxyCIDRs:    cfg.TrustedProxyCIDRs,
			TrustedProxyProvider: cfg.TrustedProxyProvider,
			Production:           cfg.AppEnv == "production",
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("HTTP server starting", "address", cfg.HTTPAddr)
		serverErrors <- server.ListenAndServe()
	}()

	shutdownContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	maintenanceDone := startMaintenanceWorker(shutdownContext, cfg.PlaywrightTestMode, func(ctx context.Context) {
		repository.RunMaintenance(ctx, pool, logger)
	})

	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-shutdownContext.Done():
		logger.Info("shutdown signal received")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			_ = server.Close()
			return err
		}
		if err := <-serverErrors; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		logger.Info("HTTP server stopped")
	}
	stop()
	if maintenanceDone != nil {
		<-maintenanceDone
	}
	return nil
}

func startMaintenanceWorker(ctx context.Context, disabled bool, worker func(context.Context)) <-chan struct{} {
	if disabled {
		return nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker(ctx)
	}()
	return done
}
