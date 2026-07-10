package bitrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Luum-Home/aisotropy/internal/tenancy"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (BSD-3), same driver as internal/facts
)

// ErrScopeMismatch is returned when a Measurement's identity does not match the
// (tenant, user) it is being saved under — a cross-tenant write the isolation
// gate forbids.
var ErrScopeMismatch = errors.New("bitrate: measurement tenant/user does not match the operation scope")

// Store is the durable, tenant-scoped bpt store: two SQLite tables, one for the
// held-out samples reserved at ingestion (bitrate_samples) and one for the bpt
// measurements the coordinator writes (bpt_measurements). It is modeled on
// internal/facts.SQLiteStore (same pure-Go driver, same busy_timeout/WAL/in-
// memory handling) and enforces the same constitutional gate: every scoped read
// and write filters by (tenant_id, user_id) validated through internal/tenancy.
type Store struct {
	db  *sql.DB
	now func() time.Time // injectable clock; defaults to time.Now
}

const schema = `
CREATE TABLE IF NOT EXISTS bitrate_samples (
    tenant_id  TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    session_id TEXT NOT NULL,
    sample_id  TEXT NOT NULL,
    text       TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, user_id, sample_id)
);
CREATE INDEX IF NOT EXISTS idx_bitrate_samples_scope
    ON bitrate_samples(tenant_id, user_id, created_at);

CREATE TABLE IF NOT EXISTS bpt_measurements (
    tenant_id   TEXT NOT NULL,
    user_id     TEXT NOT NULL,
    measured_at INTEGER NOT NULL,
    bpt_with    REAL NOT NULL,
    bpt_without REAL NOT NULL,
    lift        REAL NOT NULL,
    n_tokens    INTEGER NOT NULL,
    model_id    TEXT NOT NULL,
    sample_id   TEXT NOT NULL,
    PRIMARY KEY (tenant_id, user_id, sample_id)
);
CREATE INDEX IF NOT EXISTS idx_bpt_measurements_scope
    ON bpt_measurements(tenant_id, user_id, measured_at);
`

// OpenStore opens (or creates) a bpt store at dsn and applies the schema. dsn is
// a file path or ":memory:" for an ephemeral store. In-memory stores are pinned
// to a single connection so every query sees the same database (identical to the
// facts store).
func OpenStore(dsn string) (*Store, error) {
	inMemory := dsn == ":memory:" || strings.Contains(dsn, ":memory:")

	openDSN := dsn
	if !strings.Contains(openDSN, "?") {
		openDSN += "?_pragma=busy_timeout(5000)"
	}

	db, err := sql.Open("sqlite", openDSN)
	if err != nil {
		return nil, fmt.Errorf("bitrate: open sqlite %q: %w", dsn, err)
	}
	if inMemory {
		db.SetMaxOpenConns(1)
	} else {
		if _, err := db.Exec(`PRAGMA journal_mode=WAL;`); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("bitrate: enable WAL: %w", err)
		}
	}

	s := &Store{db: db, now: time.Now}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("bitrate: migrate schema: %w", err)
	}
	return s, nil
}

// scope validates tenant/user through internal/tenancy — the same path the facts
// tier uses — so the two tiers can never drift on what a valid identity is. It is
// the isolation gate's front door: no scoped statement runs on an unvalidated
// identity.
func scope(tenant, user string) error {
	ctx := tenancy.WithUser(tenancy.WithTenant(context.Background(), tenant), user)
	ts, err := tenancy.FromContext(ctx)
	if err != nil {
		return fmt.Errorf("bitrate: invalid scope: %w", err)
	}
	if u, _ := ts.User(); u == "" {
		return fmt.Errorf("bitrate: invalid scope: %w", errors.New("empty user"))
	}
	return nil
}

// Reserve records a held-out sample within (tenantID, userID). Idempotent on
// sample_id within scope: re-reserving the same turn (e.g. a client retry)
// upserts rather than duplicating. Satisfies SampleRecorder.
func (s *Store) Reserve(ctx context.Context, tenantID, userID, sessionID, sampleID, text string) error {
	if err := scope(tenantID, userID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO bitrate_samples (tenant_id, user_id, session_id, sample_id, text, created_at)
        VALUES (?, ?, ?, ?, ?, ?)
        ON CONFLICT(tenant_id, user_id, sample_id) DO UPDATE SET
            session_id = excluded.session_id,
            text       = excluded.text,
            created_at = excluded.created_at
    `, tenantID, userID, sessionID, sampleID, text, s.now().UnixMilli())
	if err != nil {
		return fmt.Errorf("bitrate: reserve sample %q: %w", sampleID, err)
	}
	return nil
}

// HeldOut returns up to limit most-recent held-out samples across ALL tenants,
// newest first — the one reviewed cross-tenant read (like facts.ExportAll). The
// coordinator re-partitions each sample by its own (tenant, user) before
// hydrating, so isolation holds for the measurement. Satisfies Sampler.
func (s *Store) HeldOut(ctx context.Context, limit int) ([]Sample, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
        SELECT tenant_id, user_id, session_id, sample_id, text, created_at
        FROM bitrate_samples
        ORDER BY created_at DESC, sample_id ASC
        LIMIT ?
    `, limit)
	if err != nil {
		return nil, fmt.Errorf("bitrate: held-out query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Sample
	for rows.Next() {
		var (
			sm       Sample
			createMS int64
		)
		if err := rows.Scan(&sm.TenantID, &sm.UserID, &sm.SessionID, &sm.SampleID, &sm.Text, &createMS); err != nil {
			return nil, fmt.Errorf("bitrate: held-out scan: %w", err)
		}
		sm.CreatedAt = time.UnixMilli(createMS)
		out = append(out, sm)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("bitrate: held-out rows: %w", err)
	}
	return out, nil
}

// Save upserts a measurement within its (TenantID, UserID). Idempotent on
// sample_id within scope, so re-scoring a sample refreshes rather than duplicates.
func (s *Store) Save(ctx context.Context, m Measurement) error {
	if err := scope(m.TenantID, m.UserID); err != nil {
		return err
	}
	at := m.MeasuredAt
	if at.IsZero() {
		at = s.now()
	}
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO bpt_measurements
            (tenant_id, user_id, measured_at, bpt_with, bpt_without, lift, n_tokens, model_id, sample_id)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(tenant_id, user_id, sample_id) DO UPDATE SET
            measured_at = excluded.measured_at,
            bpt_with    = excluded.bpt_with,
            bpt_without = excluded.bpt_without,
            lift        = excluded.lift,
            n_tokens    = excluded.n_tokens,
            model_id    = excluded.model_id
    `, m.TenantID, m.UserID, at.UnixMilli(), m.BptWith, m.BptWithout, m.Lift, m.NTokens, m.ModelID, m.SampleID)
	if err != nil {
		return fmt.Errorf("bitrate: save measurement %q: %w", m.SampleID, err)
	}
	return nil
}

// Latest returns the most recent measurement for (tenant, user), or ok=false if
// there is none. Scoped: the (tenant_id, user_id) predicate is the isolation gate.
func (s *Store) Latest(ctx context.Context, tenant, user string) (Measurement, bool, error) {
	if err := scope(tenant, user); err != nil {
		return Measurement{}, false, err
	}
	row := s.db.QueryRowContext(ctx, `
        SELECT tenant_id, user_id, measured_at, bpt_with, bpt_without, lift, n_tokens, model_id, sample_id
        FROM bpt_measurements
        WHERE tenant_id = ? AND user_id = ?
        ORDER BY measured_at DESC, sample_id ASC
        LIMIT 1
    `, tenant, user)
	m, err := scanMeasurement(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Measurement{}, false, nil
	}
	if err != nil {
		return Measurement{}, false, fmt.Errorf("bitrate: latest: %w", err)
	}
	return m, true, nil
}

// Series returns every measurement for (tenant, user), oldest first — the input
// to a per-tenant trend. Scoped.
func (s *Store) Series(ctx context.Context, tenant, user string) ([]Measurement, error) {
	if err := scope(tenant, user); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
        SELECT tenant_id, user_id, measured_at, bpt_with, bpt_without, lift, n_tokens, model_id, sample_id
        FROM bpt_measurements
        WHERE tenant_id = ? AND user_id = ?
        ORDER BY measured_at ASC, sample_id ASC
    `, tenant, user)
	if err != nil {
		return nil, fmt.Errorf("bitrate: series query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Measurement
	for rows.Next() {
		m, err := scanMeasurement(rows)
		if err != nil {
			return nil, fmt.Errorf("bitrate: series scan: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("bitrate: series rows: %w", err)
	}
	return out, nil
}

// TrendSlope returns the least-squares slope of lift over the tenant's ordered
// measurement series (x = 0,1,2,…). Positive means lift is trending upward — the
// per-tenant success signal (docs/research/bet-06-bits-per-token-metric.md).
// Fewer than two points yields 0 (no trend defined). Scoped.
func (s *Store) TrendSlope(ctx context.Context, tenant, user string) (float64, error) {
	series, err := s.Series(ctx, tenant, user)
	if err != nil {
		return 0, err
	}
	lifts := make([]float64, len(series))
	for i, m := range series {
		lifts[i] = m.Lift
	}
	return trendSlope(lifts), nil
}

// AllSummaries returns one aggregate per tenant across ALL tenants — the ops
// /bitrate view. This is the reviewed cross-tenant read (like facts.ExportAll);
// it is never on a request path. Each summary carries the tenant's latest
// observation plus the trend over its whole lift series (across the tenant's
// users, ordered by time).
func (s *Store) AllSummaries(ctx context.Context) ([]TenantSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT tenant_id, measured_at, bpt_with, lift, model_id
        FROM bpt_measurements
        ORDER BY tenant_id ASC, measured_at ASC, sample_id ASC
    `)
	if err != nil {
		return nil, fmt.Errorf("bitrate: summaries query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type agg struct {
		lifts         []float64
		latestLift    float64
		latestBptWith float64
		modelID       string
		n             int
	}
	order := make([]string, 0)
	byTenant := make(map[string]*agg)
	for rows.Next() {
		var (
			tenant     string
			measuredMS int64
			bptWith    float64
			lift       float64
			modelID    string
		)
		if err := rows.Scan(&tenant, &measuredMS, &bptWith, &lift, &modelID); err != nil {
			return nil, fmt.Errorf("bitrate: summaries scan: %w", err)
		}
		a, ok := byTenant[tenant]
		if !ok {
			a = &agg{}
			byTenant[tenant] = a
			order = append(order, tenant)
		}
		// Rows arrive oldest-first per tenant, so the last seen is the latest.
		a.lifts = append(a.lifts, lift)
		a.latestLift = lift
		a.latestBptWith = bptWith
		a.modelID = modelID
		a.n++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("bitrate: summaries rows: %w", err)
	}

	out := make([]TenantSummary, 0, len(order))
	for _, tenant := range order {
		a := byTenant[tenant]
		out = append(out, TenantSummary{
			TenantID:      tenant,
			LatestLift:    a.latestLift,
			LatestBptWith: a.latestBptWith,
			TrendSlope:    trendSlope(a.lifts),
			ModelID:       a.modelID,
			NMeasurements: a.n,
		})
	}
	return out, nil
}

// Close releases the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// rowScanner is the subset of *sql.Row and *sql.Rows scanMeasurement needs.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanMeasurement reads one measurement row (columns in the canonical order the
// SELECTs above use).
func scanMeasurement(r rowScanner) (Measurement, error) {
	var (
		m          Measurement
		measuredMS int64
	)
	if err := r.Scan(&m.TenantID, &m.UserID, &measuredMS, &m.BptWith, &m.BptWithout,
		&m.Lift, &m.NTokens, &m.ModelID, &m.SampleID); err != nil {
		return Measurement{}, err
	}
	m.MeasuredAt = time.UnixMilli(measuredMS)
	return m, nil
}

// trendSlope is the least-squares slope of ys over x = 0,1,…,n-1. Returns 0 for
// fewer than two points or a degenerate (zero-variance) x — the only way the
// denominator can vanish, which cannot happen for n>=2 distinct integer x.
func trendSlope(ys []float64) float64 {
	n := len(ys)
	if n < 2 {
		return 0
	}
	var sumX, sumY, sumXY, sumXX float64
	for i, y := range ys {
		x := float64(i)
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}
	fn := float64(n)
	denom := fn*sumXX - sumX*sumX
	if denom == 0 {
		return 0
	}
	return (fn*sumXY - sumX*sumY) / denom
}
