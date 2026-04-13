package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/leef-l/brain/brain-trader-v3-gpt5/internal/config"
)

const defaultDriverName = "postgres"

// PG wraps a PostgreSQL connection pool and the configured schema.
type PG struct {
	db     *sql.DB
	schema string
}

// Open connects to PostgreSQL using the default driver name.
func Open(ctx context.Context, cfg config.DatabaseConfig) (*PG, error) {
	return OpenWithDriver(ctx, defaultDriverName, cfg)
}

// OpenWithDriver connects to PostgreSQL using the provided registered driver name.
func OpenWithDriver(ctx context.Context, driverName string, cfg config.DatabaseConfig) (*PG, error) {
	if driverName == "" {
		driverName = defaultDriverName
	}
	dsn := BuildDSN(cfg)
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("storage: open db: %w", err)
	}
	if cfg.MaxConns > 0 {
		db.SetMaxOpenConns(cfg.MaxConns)
	}
	if cfg.MinConns > 0 {
		db.SetMaxIdleConns(cfg.MinConns)
	}
	if cfg.ConnMaxIdleTime > 0 {
		db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
	}
	if cfg.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	}
	pg := &PG{
		db:     db,
		schema: schemaName(cfg.Schema),
	}
	if err := pg.Ping(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return pg, nil
}

// BuildDSN builds a PostgreSQL connection string using the lib/pq style key/value format.
func BuildDSN(cfg config.DatabaseConfig) string {
	parts := []string{
		"host=" + quotePQValue(cfg.Host),
		"port=" + quotePQValue(strconv.Itoa(cfg.Port)),
		"dbname=" + quotePQValue(cfg.DBName),
		"user=" + quotePQValue(cfg.User),
	}
	if cfg.Password != "" {
		parts = append(parts, "password="+quotePQValue(cfg.Password))
	}
	if cfg.SSLMode != "" {
		parts = append(parts, "sslmode="+quotePQValue(cfg.SSLMode))
	} else {
		parts = append(parts, "sslmode=disable")
	}
	parts = append(parts, "application_name="+quotePQValue("brain-trader-v3-gpt5"))
	return strings.Join(parts, " ")
}

// DB returns the underlying *sql.DB.
func (p *PG) DB() *sql.DB {
	if p == nil {
		return nil
	}
	return p.db
}

// Schema returns the configured schema name.
func (p *PG) Schema() string {
	if p == nil || p.schema == "" {
		return "trader"
	}
	return p.schema
}

// Ping verifies that the database is reachable.
func (p *PG) Ping(ctx context.Context) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("storage: nil database")
	}
	if err := p.db.PingContext(ctx); err != nil {
		return fmt.Errorf("storage: ping: %w", err)
	}
	return nil
}

// Close releases the pool.
func (p *PG) Close() error {
	if p == nil || p.db == nil {
		return nil
	}
	return p.db.Close()
}

// Repositories creates a typed repository bundle backed by this connection.
func (p *PG) Repositories() *Repositories {
	if p == nil {
		return nil
	}
	return &Repositories{
		db:     p.db,
		schema: p.Schema(),
	}
}

func schemaName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "trader"
	}
	return name
}

func quotePQValue(value string) string {
	if value == "" {
		return ""
	}
	if strings.ContainsAny(value, " \t\n\r'\\") {
		escaped := strings.ReplaceAll(value, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, "'", `\'`)
		return "'" + escaped + "'"
	}
	return value
}
