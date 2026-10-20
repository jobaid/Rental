package config

import (
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	Port        string
	DatabaseURL string
	JWTSecret   string
	JWTTTLHours int
	CORSOrigins []string
}

func Load() Config {
	_ = godotenv.Load()
	ttl, _ := strconv.Atoi(getenv("JWT_TTL_HOURS", "24"))
	origins := strings.Split(getenv("CORS_ORIGINS", "http://localhost:5173"), ",")
	return Config{
		Port:        getenv("PORT", "8080"),
		DatabaseURL: getenv("DATABASE_URL", "postgres://rental:rental@localhost:5432/rental?sslmode=disable"),
		JWTSecret:   getenv("JWT_SECRET", "dev-insecure-secret-change-me"),
		JWTTTLHours: ttl,
		CORSOrigins: origins,
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
