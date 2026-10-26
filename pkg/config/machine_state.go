package config

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	_ "modernc.org/sqlite"
)

// Keys for the values stored in the machine state database
const (
	MachineStateKeySidePanelWidth               = "sidePanelWidth"
	MachineStateKeyHalfScreenModeSidePanelWidth = "halfScreenModeSidePanelWidth"
)

// MachineStateDB stores machine-local state in a sqlite database in the OS's
// temp folder. It holds settings that the user adjusts at runtime but hasn't
// put in their config.yml, so the adjustments survive restarting lazydocker
// on this machine without us ever writing to the user's config file. Because
// it lives in the temp folder, the OS may clean it up eventually; we treat
// that as a graceful fall back to the configured defaults.
type MachineStateDB struct {
	db *sql.DB
}

// OpenMachineStateDB opens the machine state database in the OS's temp
// folder, creating it if necessary
func OpenMachineStateDB() (*MachineStateDB, error) {
	dir := filepath.Join(os.TempDir(), "lazydocker")
	// only the current user should get to read (and tamper with) our state
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create machine state directory: %w", err)
	}

	return OpenMachineStateDBAtPath(filepath.Join(dir, "state.db"))
}

// OpenMachineStateDBAtPath opens the machine state database at the given
// path, creating it if necessary
func OpenMachineStateDBAtPath(path string) (*MachineStateDB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("failed to open machine state database: %w", err)
	}

	// if another lazydocker instance is holding the database, wait for it
	// rather than failing outright
	if _, err := db.Exec("PRAGMA busy_timeout = 2000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to configure machine state database: %w", err)
	}

	_, err = db.Exec("CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)")
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialise machine state database: %w", err)
	}

	return &MachineStateDB{db: db}, nil
}

// Get returns the value stored under the given key, and whether a value was
// present
func (m *MachineStateDB) Get(key string) (string, bool, error) {
	var value string
	err := m.db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}

	return value, true, nil
}

// GetFloat returns the float stored under the given key, and whether a valid
// value was present. An unparseable value is treated as absent.
func (m *MachineStateDB) GetFloat(key string) (float64, bool, error) {
	value, ok, err := m.Get(key)
	if err != nil || !ok {
		return 0, false, err
	}

	float, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, false, nil
	}

	return float, true, nil
}

// SetFloat stores a float under the given key
func (m *MachineStateDB) SetFloat(key string, value float64) error {
	_, err := m.db.Exec(
		"INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		key, strconv.FormatFloat(value, 'f', -1, 64),
	)
	return err
}

// Delete removes the value stored under the given key, if any
func (m *MachineStateDB) Delete(key string) error {
	_, err := m.db.Exec("DELETE FROM settings WHERE key = ?", key)
	return err
}

// Close closes the database
func (m *MachineStateDB) Close() error {
	return m.db.Close()
}
