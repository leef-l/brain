# brain-trader-v3-gpt5

This directory contains the configuration and storage foundation for the v3 trading project.

## Files

- `internal/config`: trading config structs, defaults, validation, and a YAML loader
- `internal/storage`: PostgreSQL connection wrapper and table-level repositories
- `schema.sql`: PostgreSQL schema for `trader`
- `trading.yaml.example`: example runtime config

## Quick start

1. Copy `trading.yaml.example` to `trading.yaml`.
2. Set environment variables for secrets such as `PG_PASSWORD`, `OKX_API_KEY`, `OKX_SECRET`, and `OKX_PASSPHRASE`.
3. Apply `schema.sql` to your PostgreSQL 15+ database.
4. Load config with `config.Load("trading.yaml")`.
5. Open the database with `storage.Open(ctx, cfg.Database)`.

## Notes

- The storage layer is written against `database/sql` so it compiles without a PostgreSQL driver import.
- A PostgreSQL driver still needs to be added by the main application before runtime use.
- The documented schema uses the `trader` schema and keeps the core tables in one database.
