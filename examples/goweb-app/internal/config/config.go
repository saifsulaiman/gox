package config

import (
	"cmp"
	"flag"
	"os"
)

// DefaultJWTSecret is the fallback secret used for HMAC token verification.
const DefaultJWTSecret = "nexus-enterprise-secret-key-2026"

// AppConfig encapsulates the runtime settings and multi-service connection params.
type AppConfig struct {
	Port             int
	SQLite           string
	SQLiteReplicas   []string
	Postgres         string
	PostgresReplicas []string
	PgBouncer        bool
	Redis            string
	RabbitMQ         string
	Elastic          string
	JWTSecret        string
}

// LoadConfig parses flags and environment variables to build the AppConfig.
func LoadConfig() *AppConfig {
	port := flag.Int("port", 8090, "HTTP server listening port")
	flag.Parse()

	return &AppConfig{
		Port:             *port,
		SQLite:           "file:nexus_catalog.db?cache=shared&mode=rwc",
		SQLiteReplicas:   []string{"file:nexus_catalog_slave.db?cache=shared&mode=rwc"},
		Postgres:         os.Getenv("DATABASE_URL"),
		PostgresReplicas: getEnvList("DATABASE_REPLICAS"),
		PgBouncer:        true,
		Redis:            getEnv("REDIS_ADDR", "memory"),
		RabbitMQ:         getEnv("RABBITMQ_URL", "memory"),
		Elastic:          getEnv("ELASTIC_URL", ""),
		JWTSecret:        getEnv("JWT_SECRET", DefaultJWTSecret),
	}
}

func getEnv(key, defaultValue string) string {
	return cmp.Or(os.Getenv(key), defaultValue)
}

func getEnvList(key string) []string {
	val := os.Getenv(key)
	if val == "" {
		return nil
	}
	return []string{val}
}
