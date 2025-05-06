package config

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// NagiosConfig holds Nagios configuration generation settings
type NagiosConfig struct {
	OutputDir          string `yaml:"output_dir"`
	HostTemplate       string `yaml:"host_template"`
	ServiceTemplate    string `yaml:"service_template"`
	GenerationInterval string `yaml:"generation_interval"`
	StaleThreshold     string `yaml:"stale_threshold"`
	ReloadCommand      string `yaml:"reload_command,omitempty"` // Command to execute on reload
}

// Config represents the application configuration
type Config struct {
	Server struct {
		ListenAddr string `yaml:"listen_addr"`
	} `yaml:"server"`

	Storage struct {
		OutputDir     string  `yaml:"output_dir"`
		GroupName     string  `yaml:"group_name"`
		MaxFiles      int     `yaml:"max_files"`
		MinDiskSpace  float64 `yaml:"min_disk_space_percent"`
		PauseDuration string  `yaml:"pause_duration"`
	} `yaml:"storage"`

	Logging struct {
		Level   string `yaml:"level"`
		Verbose bool   `yaml:"verbose"`
		ShowRaw bool   `yaml:"show_raw"`
	} `yaml:"logging"`

	DatabasePath string       `yaml:"database_path"`
	Nagios       NagiosConfig `yaml:"nagios"`
}

// DefaultConfig returns the default configuration
func DefaultConfig() *Config {
	cfg := &Config{}

	// Server defaults
	cfg.Server.ListenAddr = ":8080"

	// Storage defaults
	cfg.Storage.OutputDir = "/var/lib/nagios4/spool/checkresults"
	cfg.Storage.GroupName = "nagios"
	cfg.Storage.MaxFiles = 1000
	cfg.Storage.MinDiskSpace = 5.0 // 5% minimum free space
	cfg.Storage.PauseDuration = "10s"

	// Logging defaults
	cfg.Logging.Level = "info"
	cfg.Logging.Verbose = false
	cfg.Logging.ShowRaw = false

	// Database defaults
	cfg.DatabasePath = "./nrdp_checks.db" // Sensible default

	// Nagios config defaults
	cfg.Nagios.OutputDir = "/etc/nagios4/dynamic"  // Default dynamic dir
	cfg.Nagios.HostTemplate = "linux-server"       // Correct default template (singular)
	cfg.Nagios.ServiceTemplate = "generic-service" // Common default template
	cfg.Nagios.GenerationInterval = "30s"          // Default interval (30 seconds)
	cfg.Nagios.StaleThreshold = "6h"               // Default stale threshold (6 hours)

	return cfg
}

// Load loads configuration from a file
func Load(configPath string) (*Config, error) {
	// Start with defaults
	cfg := DefaultConfig()

	// If no config file specified, try to use config.yaml from current directory
	if configPath == "" {
		configPath = "config.yaml"
	}

	// Read config file
	data, err := os.ReadFile(configPath)
	if err != nil {
		// If default config.yaml wasn't found, return defaults
		if configPath == "config.yaml" {
			return cfg, nil
		}
		return nil, fmt.Errorf("error reading config file: %v", err)
	}

	// Parse YAML
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("error parsing config file: %v", err)
	}

	// --- BEGIN DEBUG LOGGING ---
	log.Printf("[DEBUG] Config loaded. Nagios OutputDir: '%s'", cfg.Nagios.OutputDir)
	log.Printf("[DEBUG] Config loaded. Nagios ReloadCommand: '%s'", cfg.Nagios.ReloadCommand)
	log.Printf("[DEBUG] Config loaded. Nagios GenInterval: '%s'", cfg.Nagios.GenerationInterval)
	log.Printf("[DEBUG] Config loaded. Nagios StaleThreshold: '%s'", cfg.Nagios.StaleThreshold)
	// --- END DEBUG LOGGING ---

	return cfg, nil
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	if c.Server.ListenAddr == "" {
		return errors.New("server listen_addr must be specified")
	}
	if c.Storage.OutputDir == "" {
		return errors.New("storage output_dir must be specified")
	}
	if c.Storage.MaxFiles < 0 {
		return errors.New("storage max_files cannot be negative")
	}
	if c.Storage.MinDiskSpace < 0 {
		return errors.New("storage min_disk_space cannot be negative")
	}
	if c.Logging.Level != "info" && c.Logging.Level != "debug" && c.Logging.Level != "trace" {
		return fmt.Errorf("invalid logging level: %s (must be info, debug, or trace)", c.Logging.Level)
	}
	if c.DatabasePath == "" {
		return errors.New("database_path must be specified")
	}
	dbDir := filepath.Dir(c.DatabasePath)
	if _, err := os.Stat(dbDir); os.IsNotExist(err) {
		// Attempt to create the directory if using default relative path
		if c.DatabasePath == "./nrdp_checks.db" {
			if err := os.MkdirAll(dbDir, 0755); err != nil {
				return fmt.Errorf("failed to create default database directory %s: %w", dbDir, err)
			}
		} else {
			return fmt.Errorf("database directory does not exist: %s", dbDir)
		}
	}

	// Check if output directory exists
	if _, err := os.Stat(c.Storage.OutputDir); os.IsNotExist(err) {
		return fmt.Errorf("output directory does not exist: %s", c.Storage.OutputDir)
	}

	// Check if output directory is absolute path
	if !filepath.IsAbs(c.Storage.OutputDir) {
		return fmt.Errorf("output directory must be an absolute path: %s", c.Storage.OutputDir)
	}

	// Validate min disk space percentage
	if c.Storage.MinDiskSpace <= 0 || c.Storage.MinDiskSpace >= 100 {
		return fmt.Errorf("min_disk_space_percent must be between 0 and 100")
	}

	// Validate max files
	if c.Storage.MaxFiles <= 0 {
		return fmt.Errorf("max_files must be greater than 0")
	}

	// Validate pause duration
	if _, err := time.ParseDuration(c.Storage.PauseDuration); err != nil {
		return fmt.Errorf("invalid storage pause_duration: %v", err)
	}

	// Validate Nagios config section
	if c.Nagios.OutputDir == "" {
		return errors.New("nagios_config output_dir must be specified")
	}
	if !filepath.IsAbs(c.Nagios.OutputDir) {
		return fmt.Errorf("nagios_config output_dir must be an absolute path: %s", c.Nagios.OutputDir)
	}
	// Check if Nagios output directory exists and is writable
	if err := checkDirWritable(c.Nagios.OutputDir); err != nil {
		return fmt.Errorf("nagios_config output_dir check failed: %w", err)
	}
	if c.Nagios.HostTemplate == "" {
		return errors.New("nagios_config host_template must be specified")
	}
	if c.Nagios.ServiceTemplate == "" {
		return errors.New("nagios_config service_template must be specified")
	}
	if _, err := time.ParseDuration(c.Nagios.GenerationInterval); err != nil {
		return fmt.Errorf("invalid nagios_config generation_interval: %v", err)
	}
	if _, err := time.ParseDuration(c.Nagios.StaleThreshold); err != nil {
		return fmt.Errorf("invalid nagios_config stale_threshold: %v", err)
	}

	// Note: No validation needed for ReloadCommand, empty means disabled.

	return nil
}

// checkDirWritable checks if a directory exists and is writable.
// Tries to create it if it doesn't exist.
func checkDirWritable(dir string) error {
	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		// Try to create the directory
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
		info, err = os.Stat(dir) // Stat again after creation
	}
	if err != nil {
		return fmt.Errorf("failed to stat directory %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("path %s is not a directory", dir)
	}

	// Try to create a temporary file to check writability
	tempFile := filepath.Join(dir, ".writetest."+fmt.Sprintf("%d", time.Now().UnixNano()))
	if err := os.WriteFile(tempFile, []byte("test"), 0644); err != nil {
		return fmt.Errorf("directory %s is not writable: %w", dir, err)
	}
	os.Remove(tempFile) // Clean up temp file
	return nil
}
