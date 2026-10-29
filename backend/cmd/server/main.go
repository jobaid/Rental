package main

import (
	"context"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/rentalsoftware/backend/internal/auth"
	"github.com/rentalsoftware/backend/internal/config"
	"github.com/rentalsoftware/backend/internal/db"
	"github.com/rentalsoftware/backend/internal/handlers"
	"github.com/rentalsoftware/backend/internal/mail"
	appmw "github.com/rentalsoftware/backend/internal/middleware"
	"github.com/rentalsoftware/backend/internal/storage"
	webui "github.com/rentalsoftware/backend/internal/web"
)

func redirectTo(url string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, url, http.StatusSeeOther) }
}

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

	store, err := storage.New(cfg.UploadDir)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}

	signer := auth.NewSigner(cfg.JWTSecret, cfg.JWTTTLHours)
	api := &handlers.Deps{DB: database, Signer: signer, Store: store, PublicBase: cfg.PublicBase}

	web, err := webui.New(database.Pool, store, []byte(cfg.JWTSecret), cfg.PublicBase)
	if err != nil {
		log.Fatalf("web: %v", err)
	}
	mailer := &mail.Sender{Cfg: mail.Config{
		Host: cfg.SMTPHost, Port: cfg.SMTPPort,
		User: cfg.SMTPUser, Pass: cfg.SMTPPass,
		FromAddr: cfg.SMTPFromAddr, FromName: cfg.SMTPFromName,
	}}
	web.SetMailer(mailer)
	if mailer.Cfg.Configured() {
		log.Printf("SMTP configured: %s:%s from %s", cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPFromAddr)
	} else {
		log.Println("SMTP not configured — messages will be saved only")
	}

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)
	r.Use(chimw.Timeout(60 * time.Second))
	r.Use(webui.Attach([]byte(cfg.JWTSecret)))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// Static assets (embedded) + uploads (disk).
	staticSub, _ := fs.Sub(webui.StaticFS, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))
	r.Handle("/uploads/*", http.StripPrefix("/uploads/", http.FileServer(http.Dir(cfg.UploadDir))))

	// Public site.
	r.Get("/", web.Home)
	r.Get("/search", web.Search)
	r.Get("/book", web.BookGet)
	r.Post("/book", web.BookPost)
	r.Get("/portal/login", web.PortalLoginGet)
	r.Post("/portal/login", web.PortalLoginPost)
	r.Post("/portal/logout", web.PortalLogoutPost)
	r.Group(func(r chi.Router) {
		r.Use(webui.RequireCustomer)
		r.Get("/portal", web.Portal)
	})
	r.Get("/upload/{token}", web.UploadGet)
	r.Post("/upload/{token}", web.UploadPost)

	// Public read-only invoice link (customer-shared).
	r.Get("/invoices/{token}", web.InvoicePublic)

	// Fleet-prep tokenised inspection workflow.
	r.Get("/fleet/{token}", web.FleetPrepGet)
	r.Post("/fleet/{token}/inspection/{kind}", web.FleetPrepSave)
	r.Post("/fleet/{token}/inspection/{kind}/photo", web.FleetPrepPhoto)
	r.Post("/fleet/{token}/photo/{pid}/delete", web.FleetPrepDeletePhoto)

	// Static public pages.
	r.Get("/about", web.About)
	r.Get("/faq", web.FAQ)
	r.Get("/terms", web.Terms)
	r.Get("/legal", web.Legal)
	r.Get("/contact", web.Contact)
	r.Post("/contact", web.ContactPost)

	// Admin site.
	r.Get("/admin/setup", web.SetupGet)
	r.Post("/admin/setup", web.SetupPost)
	r.Get("/admin/login", web.AdminLoginGet)
	r.Post("/admin/login", web.AdminLoginPost)
	r.Get("/admin/logout", web.AdminLogoutRedirect)
	r.Group(func(r chi.Router) {
		r.Use(webui.RequireAdmin)
		r.Get("/admin", web.DashboardV2)
		r.Get("/admin/dashboard-classic", web.Dashboard)
		// Legacy standalone URLs now redirect into the dashboard's tab router.
		r.Get("/admin/daily-manifest", redirectTo("/admin?view=daily-manifest"))
		r.Get("/admin/getting-started", redirectTo("/admin?view=getting-started"))
		r.Get("/admin/extras", redirectTo("/admin?view=extras"))
		r.Get("/admin/attempts", redirectTo("/admin?view=attempts"))
		r.Get("/admin/commissions", redirectTo("/admin?view=commissions"))
		r.Get("/admin/active", web.ActiveRentals)
		r.Get("/admin/reservations", web.Reservations)
		r.Get("/admin/reservations/{id}", web.ReservationDetailPage)
		r.Post("/admin/reservations/{id}/status", web.ReservationStatus)
		r.Post("/admin/reservations/{id}/checkout", web.ReservationCheckout)
		r.Post("/admin/reservations/{id}/checkin", web.ReservationCheckin)
		r.Post("/admin/reservations/{id}/payment", web.ReservationPayment)
		r.Post("/admin/reservations/{id}/photo-link", web.ReservationPhotoLink)
		r.Post("/admin/reservations/{id}/fleet-link", web.ReservationFleetLink)
		r.Post("/admin/reservations/{id}/open-thread", web.OpenThreadForReservation)
		r.Get("/admin/reservations/{id}/agreement.pdf", web.AdminAgreementPDF)
		r.Get("/admin/reservations/{id}/invoice.pdf", web.AdminInvoicePDF)

		// Invoices (pre & post).
		r.Get("/admin/invoices/{id}", web.InvoiceEditor)
		r.Post("/admin/invoices/{id}", web.InvoiceSave)
		r.Post("/admin/invoices/{id}/status", web.InvoiceStatus)
		r.Post("/admin/invoices/{id}/send", web.InvoiceSend)
		r.Get("/admin/invoices/{id}/print", web.InvoicePrint)
		r.Get("/admin/calendar", web.Calendar)
		r.Get("/admin/vehicles", web.VehiclesList)
		r.Get("/admin/vehicles/new", web.VehicleNewGet)
		r.Post("/admin/vehicles", web.VehicleNewPost)
		r.Get("/admin/vehicles/{id}", web.VehicleDetail)
		r.Post("/admin/vehicles/{id}/photos", web.VehiclePhotoUpload)
		r.Post("/admin/vehicles/{id}/photos/{pid}/delete", web.VehiclePhotoDelete)
		r.Post("/admin/vehicles/{id}/photos/{pid}/primary", web.VehiclePhotoMakePrimary)
		r.Post("/admin/vehicles/{id}/rate", web.VehicleRateUpdate)
		r.Post("/admin/vehicles/{id}/rate-periods", web.RatePeriodCreate)
		r.Post("/admin/vehicles/{id}/rate-periods/{rid}/delete", web.RatePeriodDelete)
		r.Post("/admin/vehicles/{id}/features", web.VehicleFeaturesPost)

		// Users & access.
		r.Get("/admin/users", web.UsersList)
		r.Post("/admin/users", web.UserCreate)
		r.Post("/admin/users/{id}/toggle", web.UserToggle)
		r.Post("/admin/users/{id}/role", web.UserChangeRole)
		r.Post("/admin/users/{id}/reset-password", web.UserResetPassword)

		// Account change (self-service).
		r.Post("/admin/settings/account", web.AccountPost)
		r.Get("/admin/customers", web.Customers)
		r.Get("/admin/customers/{id}", web.CustomerDetailPage)
		r.Post("/admin/customers/{id}", web.CustomerUpdate)
		r.Post("/admin/customers/{id}/delete", web.CustomerDelete)
		r.Post("/admin/customers/{id}/notes", web.CustomerNotesSave)
		r.Post("/admin/customers/{id}/relations", web.CustomerRelationAdd)
		r.Post("/admin/customers/{id}/relations/{rid}/delete", web.CustomerRelationDelete)
		r.Get("/admin/licenses", web.Licenses)
		r.Post("/admin/licenses/{id}", web.LicenseReview)
		r.Get("/admin/maintenance", web.Maintenance)
		r.Post("/admin/maintenance", web.MaintenancePost)
		r.Get("/admin/reports", web.ReportsPro)
		r.Get("/admin/reports/print", web.ReportsPrint)
		r.Get("/admin/reports.csv", web.ReportsCSVPro)
		r.Get("/admin/settings", web.Settings)
		r.Post("/admin/settings", web.SettingsPost)
		r.Post("/admin/settings/smtp", web.SMTPPost)
		r.Post("/admin/settings/smtp-test", web.SMTPTest)
		r.Post("/admin/settings/tax", web.TaxPost)

		r.Get("/admin/communications", web.Comms)
		r.Post("/admin/communications/send-email", web.CommsSendEmail)
		r.Post("/admin/communications/send-sms", web.CommsSendSMS)
		r.Post("/admin/communications/notes", web.CommsNote)
		r.Post("/admin/communications/{id}/resolve", web.CommsResolve)
		r.Get("/admin/communications/templates", web.CommsTemplates)
		r.Post("/admin/communications/templates", web.CommsTemplateSave)
		r.Get("/admin/communications/notifications", web.CommsNotifications)
		r.Post("/admin/communications/rules/{id}", web.CommsRuleUpdate)
	})

	// Existing JSON API — kept for PDF endpoints and future needs.
	r.Route("/api", func(r chi.Router) {
		r.Get("/plans", api.ListPlans)
		r.Get("/public/companies", api.PublicCompanies)
		r.Group(func(r chi.Router) {
			r.Use(appmw.RequireAuth(signer))
			r.Group(func(r chi.Router) {
				r.Use(appmw.RequireRole("company_owner", "rental_manager", "branch_manager", "reservation_agent", "fleet_manager", "technician", "accountant", "auditor"))
				r.Use(appmw.RequireCompany)
				r.Get("/manager/reservations/{id}/agreement.pdf", api.AgreementPDF)
				r.Get("/manager/reservations/{id}/invoice.pdf", api.InvoicePDF)
				r.Get("/manager/reservations/{id}/inspection.pdf", api.InspectionPDF)
			})
		})
	})

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      r,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 120 * time.Second,
	}

	go func() {
		log.Printf("listening on :%s (uploads=%s)", cfg.Port, cfg.UploadDir)
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
