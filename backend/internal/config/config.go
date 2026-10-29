package config

import (
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	Port         string
	DatabaseURL  string
	JWTSecret    string
	JWTTTLHours  int
	CORSOrigins  []string
	UploadDir    string
	StripeSecret string
	PublicBase   string

	SMTPHost     string
	SMTPPort     string
	SMTPUser     string
	SMTPPass     string
	SMTPFromAddr string
	SMTPFromName string
}

func Load() Config {
	_ = godotenv.Load()
	ttl, _ := strconv.Atoi(getenv("JWT_TTL_HOURS", "24"))
	origins := strings.Split(getenv("CORS_ORIGINS", "http://localhost:5173"), ",")
	return Config{
		Port:         getenv("PORT", "8080"),
		DatabaseURL:  getenv("DATABASE_URL", "postgres://rental:rental@localhost:5432/rental?sslmode=disable"),
		JWTSecret:    getenv("JWT_SECRET", "dev-insecure-secret-change-me"),
		JWTTTLHours:  ttl,
		CORSOrigins:  origins,
		UploadDir:    getenv("UPLOAD_DIR", "./storage"),
		StripeSecret: os.Getenv("STRIPE_SECRET"),
		PublicBase:   getenv("PUBLIC_BASE", "http://localhost:8080"),
		SMTPHost:     os.Getenv("SMTP_HOST"),
		SMTPPort:     getenv("SMTP_PORT", "587"),
		SMTPUser:     os.Getenv("SMTP_USERNAME"),
		SMTPPass:     os.Getenv("SMTP_PASSWORD"),
		SMTPFromAddr: os.Getenv("SMTP_FROM_ADDRESS"),
		SMTPFromName: getenv("SMTP_FROM_NAME", "mosrental"),
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
