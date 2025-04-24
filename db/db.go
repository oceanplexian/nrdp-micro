package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"nrdp_micro/logger" // Assuming logger package exists

	_ "github.com/mattn/go-sqlite3" // SQLite driver
)

// Host represents a row in the hosts table
type Host struct {
	Hostname string
	LastSeen time.Time
}

// Service represents a row in the services table
type Service struct {
	Hostname           string
	ServiceDescription string
	LastSeen           time.Time
}

// Manager handles database operations
type Manager struct {
	db *sql.DB
}

// NewManager creates a new database manager and initializes the database.
func NewManager(dbPath string) (*Manager, error) {
	logger.Logf(logger.LevelDebug, "Initializing database at %s", dbPath)

	// Ensure the directory exists
	dbDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory %s: %w", dbDir, err)
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database %s: %w", dbPath, err)
	}

	m := &Manager{db: db}

	if err := m.initSchema(); err != nil {
		db.Close() // Close the connection if schema init fails
		return nil, fmt.Errorf("failed to initialize database schema: %w", err)
	}

	logger.Logf(logger.LevelInfo, "Database initialized successfully at %s", dbPath)
	return m, nil
}

// initSchema creates the necessary tables if they don't exist.
func (m *Manager) initSchema() error {
	createHostsTable := `
	CREATE TABLE IF NOT EXISTS hosts (
		hostname TEXT PRIMARY KEY,
		last_seen INTEGER NOT NULL
	);`
	_, err := m.db.Exec(createHostsTable)
	if err != nil {
		return fmt.Errorf("failed to create hosts table: %w", err)
	}
	logger.Logf(logger.LevelDebug, "Hosts table checked/created.")

	createServicesTable := `
	CREATE TABLE IF NOT EXISTS services (
		hostname TEXT NOT NULL,
		service_description TEXT NOT NULL,
		last_seen INTEGER NOT NULL,
		PRIMARY KEY (hostname, service_description)
	);`
	_, err = m.db.Exec(createServicesTable)
	if err != nil {
		return fmt.Errorf("failed to create services table: %w", err)
	}
	logger.Logf(logger.LevelDebug, "Services table checked/created.")

	return nil
}

// UpdateHost updates the last_seen timestamp for a given host.
// If the host doesn't exist, it's inserted.
func (m *Manager) UpdateHost(hostname string, lastSeen time.Time) error {
	unixTime := lastSeen.Unix()
	query := `
	INSERT INTO hosts (hostname, last_seen)
	VALUES (?, ?)
	ON CONFLICT(hostname) DO UPDATE SET last_seen = excluded.last_seen;
	`
	_, err := m.db.Exec(query, hostname, unixTime)
	if err != nil {
		logger.Logf(logger.LevelDebug, "Failed to update host %s: %v", hostname, err)
		return fmt.Errorf("failed to update host %s: %w", hostname, err)
	}
	logger.Logf(logger.LevelTrace, "Updated host %s last_seen to %d", hostname, unixTime)
	return nil
}

// UpdateService updates the last_seen timestamp for a given host and service description.
// If the service doesn't exist, it's inserted.
func (m *Manager) UpdateService(hostname, serviceDescription string, lastSeen time.Time) error {
	unixTime := lastSeen.Unix()
	query := `
	INSERT INTO services (hostname, service_description, last_seen)
	VALUES (?, ?, ?)
	ON CONFLICT(hostname, service_description) DO UPDATE SET last_seen = excluded.last_seen;
	`
	_, err := m.db.Exec(query, hostname, serviceDescription, unixTime)
	if err != nil {
		logger.Logf(logger.LevelDebug, "Failed to update service '%s' on host %s: %v", serviceDescription, hostname, err)
		return fmt.Errorf("failed to update service '%s' on host %s: %w", serviceDescription, hostname, err)
	}
	logger.Logf(logger.LevelTrace, "Updated service '%s' on host %s last_seen to %d", serviceDescription, hostname, unixTime)
	return nil
}

// GetAllHosts retrieves all hosts from the database.
func (m *Manager) GetAllHosts() ([]Host, error) {
	query := `SELECT hostname, last_seen FROM hosts ORDER BY hostname;`
	rows, err := m.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query hosts: %w", err)
	}
	defer rows.Close()

	var hosts []Host
	for rows.Next() {
		var h Host
		var lastSeenUnix int64
		if err := rows.Scan(&h.Hostname, &lastSeenUnix); err != nil {
			return nil, fmt.Errorf("failed to scan host row: %w", err)
		}
		h.LastSeen = time.Unix(lastSeenUnix, 0)
		hosts = append(hosts, h)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error during host rows iteration: %w", err)
	}

	return hosts, nil
}

// GetAllServices retrieves all services from the database.
func (m *Manager) GetAllServices() ([]Service, error) {
	query := `SELECT hostname, service_description, last_seen FROM services ORDER BY hostname, service_description;`
	rows, err := m.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query services: %w", err)
	}
	defer rows.Close()

	var services []Service
	for rows.Next() {
		var s Service
		var lastSeenUnix int64
		if err := rows.Scan(&s.Hostname, &s.ServiceDescription, &lastSeenUnix); err != nil {
			return nil, fmt.Errorf("failed to scan service row: %w", err)
		}
		s.LastSeen = time.Unix(lastSeenUnix, 0)
		services = append(services, s)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error during service rows iteration: %w", err)
	}

	return services, nil
}

// DeleteStaleHosts removes hosts whose last_seen time is older than the threshold.
func (m *Manager) DeleteStaleHosts(threshold time.Time) (int64, error) {
	query := `DELETE FROM hosts WHERE last_seen < ?;`
	result, err := m.db.Exec(query, threshold.Unix())
	if err != nil {
		logger.Logf(logger.LevelInfo, "Error executing delete stale hosts query: %v", err)
		return 0, fmt.Errorf("failed to execute delete stale hosts query: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		// This might happen on drivers that don't support RowsAffected well, log but don't fail
		logger.Logf(logger.LevelDebug, "Could not get rows affected after deleting stale hosts: %v", err)
		return 0, nil // Return 0 affected, but no error
	}
	if rowsAffected > 0 {
		logger.Logf(logger.LevelDebug, "Deleted %d stale hosts (older than %s)", rowsAffected, threshold.Format(time.RFC3339))
	}
	return rowsAffected, nil
}

// DeleteStaleServices removes services whose last_seen time is older than the threshold.
func (m *Manager) DeleteStaleServices(threshold time.Time) (int64, error) {
	query := `DELETE FROM services WHERE last_seen < ?;`
	result, err := m.db.Exec(query, threshold.Unix())
	if err != nil {
		logger.Logf(logger.LevelInfo, "Error executing delete stale services query: %v", err)
		return 0, fmt.Errorf("failed to execute delete stale services query: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		logger.Logf(logger.LevelDebug, "Could not get rows affected after deleting stale services: %v", err)
		return 0, nil
	}
	if rowsAffected > 0 {
		logger.Logf(logger.LevelDebug, "Deleted %d stale services (older than %s)", rowsAffected, threshold.Format(time.RFC3339))
	}
	return rowsAffected, nil
}

// Close closes the database connection.
func (m *Manager) Close() error {
	if m.db != nil {
		logger.Logf(logger.LevelDebug, "Closing database connection.")
		return m.db.Close()
	}
	return nil
}
