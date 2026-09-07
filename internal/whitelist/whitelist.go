// Package whitelist stores the server's IP-whitelist firewall rules in a
// SQLite database and answers accept-time membership queries from an
// in-memory prefix cache (design.md §4).
//
// Rules are either exact IPs ("1.2.3.4") or CIDR blocks ("10.0.0.0/8");
// IPv4 and IPv6 both work via net/netip. The cache is refreshed on every
// write, so the database stays the source of truth while accept-time lookups
// never touch the disk.
package whitelist

import (
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, cgo-free (cross-compile friendly)
)

// Rule is one stored whitelist entry.
type Rule struct {
	ID        int64  `json:"id"`
	Rule      string `json:"rule"` // normalized: "1.2.3.4" or masked CIDR "10.0.0.0/8"
	CreatedAt string `json:"created_at"`
}

// Store wraps the SQLite database plus the in-memory prefix cache.
type Store struct {
	db       *sql.DB
	mu       sync.RWMutex
	prefixes []netip.Prefix // cached, refreshed on every write
}

// Open opens (creating if needed) the whitelist database at path.
// ":memory:" yields a private in-memory database (used by tests).
func Open(path string) (*Store, error) {
	if path != ":memory:" {
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("whitelist: create dir %s: %w", dir, err)
			}
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("whitelist: open %s: %w", path, err)
	}
	// One connection: modernc's :memory: databases are per-connection, and a
	// single writer also sidesteps SQLITE_BUSY on file-backed stores.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.refresh(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) init() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS rules (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		rule       TEXT NOT NULL UNIQUE,
		created_at TEXT NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("whitelist: migrate: %w", err)
	}
	return nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// ParseRule validates and normalizes one rule string ("1.2.3.4" or CIDR).
func ParseRule(rule string) (string, error) {
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return "", errors.New("rule is empty")
	}
	if strings.Contains(rule, "/") {
		p, err := netip.ParsePrefix(rule)
		if err != nil {
			return "", fmt.Errorf("invalid CIDR %q", rule)
		}
		return p.Masked().String(), nil
	}
	addr, err := netip.ParseAddr(rule)
	if err != nil {
		return "", fmt.Errorf("invalid IP %q", rule)
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String(), nil
	}
	return addr.String(), nil // IPv6 literal, no CIDR bits
}

// toPrefix converts a stored normalized rule into the prefix used by Contains.
func toPrefix(rule string) (netip.Prefix, error) {
	if strings.Contains(rule, "/") {
		return netip.ParsePrefix(rule)
	}
	addr, err := netip.ParseAddr(rule)
	if err != nil {
		return netip.Prefix{}, err
	}
	addr = addr.Unmap()
	bits := 32
	if addr.Is6() {
		bits = 128
	}
	return netip.PrefixFrom(addr, bits), nil
}

// Add inserts a rule (idempotent: re-adding an existing rule returns the
// existing entry) and refreshes the cache.
func (s *Store) Add(rule string) (Rule, error) {
	norm, err := ParseRule(rule)
	if err != nil {
		return Rule{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.Exec(
		`INSERT INTO rules (rule, created_at) VALUES (?, ?)
		 ON CONFLICT(rule) DO NOTHING`, norm, now)
	if err != nil {
		return Rule{}, fmt.Errorf("whitelist: insert: %w", err)
	}
	var id int64
	if n, _ := res.RowsAffected(); n == 0 {
		if err := s.db.QueryRow(`SELECT id, created_at FROM rules WHERE rule = ?`, norm).
			Scan(&id, &now); err != nil {
			return Rule{}, fmt.Errorf("whitelist: lookup after insert: %w", err)
		}
	} else {
		id, _ = res.LastInsertId()
	}
	if err := s.refresh(); err != nil {
		return Rule{}, err
	}
	return Rule{ID: id, Rule: norm, CreatedAt: now}, nil
}

// Delete removes rule id. It reports whether a row was removed.
func (s *Store) Delete(id int64) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM rules WHERE id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("whitelist: delete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		if err := s.refresh(); err != nil {
			return false, err
		}
	}
	return n > 0, nil
}

// List returns all rules ordered by id.
func (s *Store) List() ([]Rule, error) {
	rows, err := s.db.Query(`SELECT id, rule, created_at FROM rules ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("whitelist: list: %w", err)
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.ID, &r.Rule, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Count returns the number of stored rules.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.prefixes)
}

// Contains reports whether ip is matched by any rule. Invalid addrs (e.g.
// from a connection we could not parse) are never allowed.
func (s *Store) Contains(ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	ip = ip.Unmap()
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// refresh reloads the prefix cache from the database.
func (s *Store) refresh() error {
	rows, err := s.db.Query(`SELECT rule FROM rules`)
	if err != nil {
		return fmt.Errorf("whitelist: reload: %w", err)
	}
	defer rows.Close()
	prefixes := make([]netip.Prefix, 0, 8)
	for rows.Next() {
		var rule string
		if err := rows.Scan(&rule); err != nil {
			return err
		}
		p, err := toPrefix(rule)
		if err != nil {
			return fmt.Errorf("whitelist: corrupt rule %q: %w", rule, err)
		}
		prefixes = append(prefixes, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.prefixes = prefixes
	s.mu.Unlock()
	return nil
}
