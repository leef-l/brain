package storage

import (
	"strings"
	"testing"

	"github.com/leef-l/brain/brain-trader-v3-gpt5/internal/config"
)

func TestBuildDSN(t *testing.T) {
	dsn := BuildDSN(config.DatabaseConfig{
		Host:     "127.0.0.1",
		Port:     5432,
		DBName:   "brain_trader",
		User:     "trader",
		Password: "secret pass",
		SSLMode:  "disable",
	})

	for _, want := range []string{
		"host=127.0.0.1",
		"port=5432",
		"dbname=brain_trader",
		"user=trader",
		"password='secret pass'",
		"sslmode=disable",
		"application_name=brain-trader-v3-gpt5",
	} {
		if !strings.Contains(dsn, want) {
			t.Fatalf("DSN %q does not contain %q", dsn, want)
		}
	}
}
