// Package persistence — driver.go defines the pluggable database driver
// abstraction for the Brain SDK persistence layer.
//
// The design follows the database/sql.Register pattern: third-party packages
// import a driver side-effect package (e.g. _ "github.com/leef-l/brain/persistence/sqlite")
// which calls persistence.Register in its init(). User code then calls
// persistence.Open("sqlite", dsn) to obtain a fully-wired Stores bundle.
//
// Built-in drivers:
//   - "mem"  — in-process maps, zero allocation, for tests and `brain doctor`
//   - "file" — JSON file on disk, zero-dependency persistent backend
//
// External drivers (separate packages to avoid pulling in CGo / large deps):
//   - "sqlite"   — SQLite WAL via modernc.org/sqlite (pure Go)
//   - "mysql"    — MySQL 5.7+ / 8.x via go-sql-driver/mysql
//   - "postgres" — PostgreSQL 12+ via lib/pq or pgx
package persistence

import (
	"fmt"
	"sort"
	"sync"
)

// Stores is the bundle of all persistence interfaces that a Driver must
// provide. Kernel construction consumes a Stores value to wire every
// persistence slot in one shot.
//
// A Driver may return nil for ArtifactStore / ArtifactMeta if the backend
// delegates CAS storage to a separate system (e.g. S3); the caller is
// responsible for checking nil before use.
type Stores struct {
	PlanStore          PlanStore
	ArtifactStore      ArtifactStore
	ArtifactMeta       ArtifactMetaStore
	RunCheckpointStore RunCheckpointStore
	UsageLedger        UsageLedger
	ResumeCoordinator  ResumeCoordinator
}

// Driver is the interface that database backends must implement to
// participate in the persistence registry.
//
// A Driver is a factory: given a DSN (Data Source Name), it opens a
// connection and returns the complete Stores bundle. The DSN format is
// driver-specific:
//   - "mem"  driver: DSN is ignored (pass "")
//   - "file" driver: DSN is the file path (e.g. "/tmp/brain.json")
//   - SQL drivers: standard connection string
type Driver interface {
	// Open creates or connects to a persistence backend identified by dsn
	// and returns the full Stores bundle. The caller owns the returned
	// stores and must call Close when done.
	Open(dsn string) (*Stores, error)
}

// DriverCloser is an optional interface that Stores bundles may carry.
// If the Stores value returned by Driver.Open also implements DriverCloser,
// the caller SHOULD invoke Close during graceful shutdown.
type DriverCloser interface {
	Close() error
}

// ClosableStores extends Stores with an optional Close method for drivers
// that hold resources (DB connections, file handles).
type ClosableStores struct {
	Stores
	closer func() error
}

// Close releases resources held by the driver. Safe to call on nil receiver.
func (cs *ClosableStores) Close() error {
	if cs == nil || cs.closer == nil {
		return nil
	}
	return cs.closer()
}

// NewClosableStores wraps a Stores bundle with a close function.
func NewClosableStores(s Stores, closeFn func() error) *ClosableStores {
	return &ClosableStores{Stores: s, closer: closeFn}
}

// ── Global driver registry ──────────────────────────────────────────────

var (
	driversMu sync.RWMutex
	drivers   = make(map[string]Driver)
)

// Register makes a persistence driver available by name.
// If Register is called twice with the same name, or if driver is nil,
// it panics — following the database/sql convention.
func Register(name string, driver Driver) {
	driversMu.Lock()
	defer driversMu.Unlock()

	if driver == nil {
		panic("persistence: Register driver is nil")
	}
	if _, dup := drivers[name]; dup {
		panic("persistence: Register called twice for driver " + name)
	}
	drivers[name] = driver
}

// Drivers returns a sorted list of registered driver names.
func Drivers() []string {
	driversMu.RLock()
	defer driversMu.RUnlock()

	names := make([]string, 0, len(drivers))
	for name := range drivers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Open opens a persistence backend using the named driver and DSN.
// It returns a ClosableStores so the caller can release resources.
//
// Usage:
//
//	import _ "github.com/leef-l/brain/persistence/sqlite" // register driver
//
//	stores, err := persistence.Open("sqlite", "file:brain.db?_journal=WAL")
//	if err != nil { ... }
//	defer stores.Close()
func Open(driverName, dsn string) (*ClosableStores, error) {
	driversMu.RLock()
	d, ok := drivers[driverName]
	driversMu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("persistence: unknown driver %q (forgotten import?)", driverName)
	}

	s, err := d.Open(dsn)
	if err != nil {
		return nil, fmt.Errorf("persistence: driver %q open: %w", driverName, err)
	}

	// Check if the driver returned a ClosableStores directly.
	if cs, ok := interface{}(s).(DriverCloser); ok {
		return NewClosableStores(*s, cs.Close), nil
	}
	return NewClosableStores(*s, nil), nil
}

// MustOpen is like Open but panics on error. Intended for use in init()
// or test setup where failure is not recoverable.
func MustOpen(driverName, dsn string) *ClosableStores {
	cs, err := Open(driverName, dsn)
	if err != nil {
		panic(err)
	}
	return cs
}

// ── Kernel integration helper ───────────────────────────────────────────

// KernelOptions returns a slice of kernel.Option-compatible functions that
// wire every non-nil store from the Stores bundle into a Kernel.
// This is intentionally typed as []func(*interface{}) to avoid an import
// cycle with the kernel package — the kernel package provides the actual
// adapter: kernel.WithStores(stores).
//
// See kernel.WithPersistence for the idiomatic way to use this.
