// Package syncdb is the server half of the offline sync engine: the list of
// synced tables, the per-household write lock that makes commit order equal
// rev order, rev-ordered pulls with tombstones, and change notifications.
package syncdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Table is a synced table. Global tables also hold shared rows
// (household_id IS NULL) that every household reads.
type Table struct {
	Name   string
	Global bool
}

// Tables are every synced table, in a stable order. All carry id,
// household_id, rev and deleted_at. Adding one needs a migration and a
// Dexie version on the client.
var Tables = []Table{
	{"category", true},
	{"ingredient", true},
	{"store", false},
	{"location", false},
	{"product", false},
	{"product_barcode", false},
	{"product_stat", false},
	{"stock_item", false},
	{"stock_event", false},
	{"shopping_list", false},
	{"shopping_item", false},
	{"recipe", false},
	{"recipe_version", false},
	{"recipe_photo", false},
	{"cook_session", false},
	{"recipe_import", false},
	{"substitution", true},
}

const globalLockKey = "hh:global"

// LockHousehold serializes writers of household-synced rows. Every
// transaction that writes them must call it first: rev is drawn from a
// sequence, and without the lock a slow transaction could commit a lower rev
// after a client's cursor passed it, hiding the row forever.
func LockHousehold(ctx context.Context, tx pgx.Tx, household uuid.UUID) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", "hh:"+household.String())
	return err
}

// LockGlobal is LockHousehold for global catalog rows (household_id NULL).
func LockGlobal(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", globalLockKey)
	return err
}

// Channel returns the LISTEN channel notified when household's rows change;
// a nil household means the global catalog.
func Channel(household *uuid.UUID) string {
	if household == nil {
		return "sync_global"
	}
	return "sync_" + strings.ReplaceAll(household.String(), "-", "")
}

// Page is one pull: rows in rev order, tombstones for deleted rows, and the
// cursor to continue from.
type Page struct {
	Rows       map[string][]json.RawMessage
	Tombstones map[string][]uuid.UUID
	Next       int64
	More       bool
}

// MaxStreams caps concurrent change streams per household: each holds a
// Postgres connection, and devices that reconnect in a loop shouldn't pile up.
const MaxStreams = 8

// ErrTooManyStreams is returned by Listen at the cap.
var ErrTooManyStreams = errors.New("syncdb: too many open change streams")

// Store reads synced rows.
type Store struct {
	pool    *pgxpool.Pool
	extract map[string]string // table → jsonb expression rendering numerics as strings

	mu      sync.Mutex
	streams map[uuid.UUID]int
}

// New introspects the synced tables so numeric columns go over the wire as
// decimal strings, as the contract says.
func New(ctx context.Context, pool *pgxpool.Pool) (*Store, error) {
	s := &Store{pool: pool, extract: map[string]string{}, streams: map[uuid.UUID]int{}}
	rows, err := pool.Query(ctx, `
		SELECT c.relname, a.attname
		FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid
		WHERE c.relnamespace = 'public'::regnamespace AND c.relname = ANY($1)
		  AND a.attnum > 0 AND NOT a.attisdropped AND a.atttypid = 'numeric'::regtype
		ORDER BY 1, 2`, tableNames())
	if err != nil {
		return nil, fmt.Errorf("syncdb: introspect: %w", err)
	}
	defer rows.Close()
	numerics := map[string][]string{}
	for rows.Next() {
		var t, c string
		if err := rows.Scan(&t, &c); err != nil {
			return nil, err
		}
		numerics[t] = append(numerics[t], c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, t := range Tables {
		expr := "to_jsonb(t)"
		if cols := numerics[t.Name]; len(cols) > 0 {
			var parts []string
			for _, c := range cols {
				parts = append(parts, fmt.Sprintf("'%s', t.%s::text", c, c))
			}
			expr += " || jsonb_strip_nulls(jsonb_build_object(" + strings.Join(parts, ", ") + "))"
		}
		s.extract[t.Name] = expr
	}
	return s, nil
}

func tableNames() []string {
	out := make([]string, len(Tables))
	for i, t := range Tables {
		out[i] = t.Name
	}
	return out
}

type change struct {
	table   string
	rev     int64
	id      uuid.UUID
	deleted bool
	row     json.RawMessage
}

// Pull returns up to limit changes with rev > since, oldest first, from one
// snapshot. household nil reads the global catalog rows (household_id NULL,
// only in global tables); otherwise that household's rows in every table.
func (s *Store) Pull(ctx context.Context, household *uuid.UUID, since int64, limit int) (Page, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Page{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var all []change
	for _, t := range Tables {
		if household == nil && !t.Global {
			continue
		}
		where := "t.household_id = $1"
		var arg any = household
		if household == nil {
			where = "t.household_id IS NULL AND $1::uuid IS NULL"
		}
		q := fmt.Sprintf(`SELECT t.rev, t.id, t.deleted_at IS NOT NULL,
			CASE WHEN t.deleted_at IS NULL THEN %s END
			FROM %s t WHERE %s AND t.rev > $2 ORDER BY t.rev LIMIT $3`, s.extract[t.Name], t.Name, where)
		rows, err := tx.Query(ctx, q, arg, since, limit+1)
		if err != nil {
			return Page{}, fmt.Errorf("syncdb: pull %s: %w", t.Name, err)
		}
		for rows.Next() {
			c := change{table: t.Name}
			if err := rows.Scan(&c.rev, &c.id, &c.deleted, &c.row); err != nil {
				rows.Close()
				return Page{}, err
			}
			all = append(all, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return Page{}, err
		}
	}
	slices.SortFunc(all, func(a, b change) int {
		switch {
		case a.rev < b.rev:
			return -1
		case a.rev > b.rev:
			return 1
		}
		return 0
	})
	p := Page{Rows: map[string][]json.RawMessage{}, Tombstones: map[string][]uuid.UUID{}, Next: since}
	if len(all) > limit {
		p.More = true
		all = all[:limit]
	}
	for _, c := range all {
		p.Next = c.rev
		if c.deleted {
			p.Tombstones[c.table] = append(p.Tombstones[c.table], c.id)
		} else {
			p.Rows[c.table] = append(p.Rows[c.table], c.row)
		}
	}
	return p, nil
}

// Listen subscribes to change notifications for household and the global
// catalog. Receives are coalesced: a burst of changes yields one signal.
// Call stop to release the dedicated connection. The connection is not
// taken from the pool, so idle SSE clients can't starve requests.
func (s *Store) Listen(ctx context.Context, household uuid.UUID) (changes <-chan struct{}, stop func(), err error) {
	s.mu.Lock()
	if s.streams[household] >= MaxStreams {
		s.mu.Unlock()
		return nil, nil, ErrTooManyStreams
	}
	s.streams[household]++
	s.mu.Unlock()
	release := func() {
		s.mu.Lock()
		if s.streams[household]--; s.streams[household] <= 0 {
			delete(s.streams, household)
		}
		s.mu.Unlock()
	}
	conn, err := pgx.ConnectConfig(ctx, s.pool.Config().ConnConfig)
	if err != nil {
		release()
		return nil, nil, err
	}
	hh := household
	for _, ch := range []string{Channel(&hh), Channel(nil)} {
		if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{ch}.Sanitize()); err != nil {
			_ = conn.Close(ctx)
			release()
			return nil, nil, err
		}
	}
	lctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	out := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(out)
		for {
			if _, err := conn.WaitForNotification(lctx); err != nil {
				return
			}
			select {
			case out <- struct{}{}:
			default:
			}
		}
	}()
	return out, func() {
		cancel()
		<-done
		cctx, ccancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer ccancel()
		_ = conn.Close(cctx)
		release()
	}, nil
}
