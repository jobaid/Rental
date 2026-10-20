package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/rentalsoftware/backend/internal/auth"
	"github.com/rentalsoftware/backend/internal/config"
	"github.com/rentalsoftware/backend/internal/db"
	"github.com/rentalsoftware/backend/internal/handlers"
	appmw "github.com/rentalsoftware/backend/internal/middleware"
)

func main() {
	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	database, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db open: %v", err)
	}
	defer database.Close()

	if err := database.Migrate(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	signer := auth.NewSigner(cfg.JWTSecret, cfg.JWTTTLHours)
	deps := &handlers.Deps{DB: database, Signer: signer}

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)
	r.Use(chimw.Timeout(30 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   cfg.CORSOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	// Health & liveness.
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	r.Route("/api", func(r chi.Router) {
		// Public endpoints.
		r.Post("/auth/login", deps.Login)
		r.Post("/auth/bootstrap-owner", deps.BootstrapOwner)
		r.Get("/plans", deps.ListPlans)

		// Authenticated.
		r.Group(func(r chi.Router) {
			r.Use(appmw.RequireAuth(signer))
			r.Get("/me", deps.Me)

			// Platform-owner only (SaaS admin).
			r.Group(func(r chi.Router) {
				r.Use(appmw.RequireRole("platform_owner"))
				r.Post("/owner/companies", deps.CreateCompany)
				r.Get("/owner/companies", deps.ListCompanies)
				r.Get("/owner/stats", deps.OwnerStats)
			})

			// Tenant-scoped (company users).
			r.Group(func(r chi.Router) {
				r.Use(appmw.RequireRole(
					"company_owner", "rental_manager", "branch_manager",
					"reservation_agent", "fleet_manager", "technician",
					"accountant", "auditor",
				))
				r.Use(appmw.RequireCompany)
				r.Get("/manager/stats", deps.ManagerStats)
				r.Get("/manager/branches", deps.ListBranches)
				r.Post("/manager/branches", deps.CreateBranch)
				r.Get("/manager/vehicles", deps.ListVehicles)
				r.Post("/manager/vehicles", deps.CreateVehicle)
			})
		})
	})

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	go func() {
		log.Printf("listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
