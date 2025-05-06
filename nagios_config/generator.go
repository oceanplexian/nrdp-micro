package nagios_config

import (
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"nrdp_micro/config"
	"nrdp_micro/db"
	"nrdp_micro/logger"
)

// Generator handles the generation of Nagios config files.
type Generator struct {
	config         *config.NagiosConfig
	db             *db.Manager
	interval       time.Duration
	staleThreshold time.Duration
	ReloadChan     chan struct{} // Channel to signal config reload
}

// NewGenerator creates a new Nagios config generator.
func NewGenerator(cfg *config.NagiosConfig, dbManager *db.Manager) (*Generator, error) {
	interval, err := time.ParseDuration(cfg.GenerationInterval)
	if err != nil {
		return nil, fmt.Errorf("invalid generation interval: %w", err)
	}
	staleThreshold, err := time.ParseDuration(cfg.StaleThreshold)
	if err != nil {
		return nil, fmt.Errorf("invalid stale threshold: %w", err)
	}

	return &Generator{
		config:         cfg,
		db:             dbManager,
		interval:       interval,
		staleThreshold: staleThreshold,
		ReloadChan:     make(chan struct{}), // Initialize the channel
	}, nil
}

// Start runs the generator periodically in a goroutine.
func (g *Generator) Start() {
	logger.Logf(logger.LevelInfo, "Starting Nagios config generator (interval: %s, stale after: %s, output: %s)", g.interval, g.staleThreshold, g.config.OutputDir)
	ticker := time.NewTicker(g.interval)
	go func() {
		// Generate once immediately on start
		g.generateConfigs()
		for range ticker.C {
			g.generateConfigs()
		}
	}()
}

// generateConfigs fetches data from DB and writes Nagios config files.
func (g *Generator) generateConfigs() {
	logger.Logf(logger.LevelDebug, "Running Nagios config generation cycle...")

	// 1. Delete stale entries from DB
	staleCutoff := time.Now().Add(-g.staleThreshold)
	deletedHosts, err := g.db.DeleteStaleHosts(staleCutoff)
	if err != nil {
		logger.Logf(logger.LevelInfo, "Error deleting stale hosts: %v", err)
		// Continue if possible, as services might still be deletable
	}
	deletedServices, err := g.db.DeleteStaleServices(staleCutoff)
	if err != nil {
		logger.Logf(logger.LevelInfo, "Error deleting stale services: %v", err)
		// Don't necessarily stop, generating with remaining data might be okay
	}
	if deletedHosts > 0 || deletedServices > 0 {
		logger.Logf(logger.LevelInfo, "Pruned %d stale hosts and %d stale services older than %s", deletedHosts, deletedServices, staleCutoff.Format(time.RFC3339))
	}

	// 2. Fetch currently active hosts and services
	hosts, err := g.db.GetAllHosts()
	if err != nil {
		logger.Logf(logger.LevelInfo, "Error getting hosts from DB after pruning: %v", err)
		return // Stop if we can't get hosts
	}

	services, err := g.db.GetAllServices()
	if err != nil {
		logger.Logf(logger.LevelInfo, "Error getting services from DB after pruning: %v", err)
		return // Stop if we can't get services
	}

	if len(hosts) == 0 && len(services) == 0 {
		logger.Logf(logger.LevelDebug, "No active hosts or services found in DB, skipping config generation.")
		// Optionally: write an empty config file or delete the existing one?
		// For now, just skip generation.
		return
	}

	// 3. Generate config content for active entries
	// Group services by hostname
	servicesByHost := make(map[string][]db.Service)
	for _, s := range services {
		servicesByHost[s.Hostname] = append(servicesByHost[s.Hostname], s)
	}

	// Generate config content
	var buffer bytes.Buffer

	// Ensure hosts are sorted for consistent file output
	sort.Slice(hosts, func(i, j int) bool {
		return hosts[i].Hostname < hosts[j].Hostname
	})

	for _, h := range hosts {
		// Write host definition
		buffer.WriteString(fmt.Sprintf("define host {\n"))
		buffer.WriteString(fmt.Sprintf("    use                 %s\n", g.config.HostTemplate))
		buffer.WriteString(fmt.Sprintf("    host_name           %s\n", h.Hostname))
		buffer.WriteString(fmt.Sprintf("    alias               %s\n", h.Hostname)) // Use hostname as alias for simplicity
		buffer.WriteString(fmt.Sprintf("}\n\n"))

		// Write service definitions for this host
		if hostServices, ok := servicesByHost[h.Hostname]; ok {
			// Sort services for consistency
			sort.Slice(hostServices, func(i, j int) bool {
				return hostServices[i].ServiceDescription < hostServices[j].ServiceDescription
			})
			for _, s := range hostServices {
				buffer.WriteString(fmt.Sprintf("define service {\n"))
				buffer.WriteString(fmt.Sprintf("    use                     %s\n", g.config.ServiceTemplate))
				buffer.WriteString(fmt.Sprintf("    host_name               %s\n", s.Hostname))
				buffer.WriteString(fmt.Sprintf("    service_description     %s\n", s.ServiceDescription))
				buffer.WriteString(fmt.Sprintf("    check_command           check_dummy!0!OK\n")) // Passive check
				buffer.WriteString(fmt.Sprintf("    active_checks_enabled   0\n"))
				buffer.WriteString(fmt.Sprintf("    passive_checks_enabled  1\n"))
				buffer.WriteString(fmt.Sprintf("    check_freshness         1\n"))
				buffer.WriteString(fmt.Sprintf("    freshness_threshold     %d\n", int(g.staleThreshold.Seconds()))) // Use StaleThreshold for freshness?
				buffer.WriteString(fmt.Sprintf("    notification_interval   0\n"))
				buffer.WriteString(fmt.Sprintf("}\n\n"))
			}
		}
	}

	// Get the new config content
	newConfigContent := buffer.Bytes()

	// Paths
	tempFileName := filepath.Join(g.config.OutputDir, "nrdp_generated.cfg.tmp")
	finalFileName := filepath.Join(g.config.OutputDir, "nrdp_generated.cfg")

	// Read current config content for comparison
	existingConfigContent, err := ioutil.ReadFile(finalFileName)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		// Log error if it's not just "file does not exist"
		logger.Logf(logger.LevelInfo, "Error reading existing Nagios config file %s for comparison: %v", finalFileName, err)
		// Continue, maybe the file is just missing or permissions issue.
	}

	// Compare new content with existing content
	if bytes.Equal(newConfigContent, existingConfigContent) {
		logger.Logf(logger.LevelDebug, "Generated Nagios config is identical to the existing one (%s). Skipping write and reload signal.", finalFileName)
		return // No change, do nothing further
	}

	// Content has changed, proceed with writing and signaling

	// Write the new config to a temporary file
	if err := ioutil.WriteFile(tempFileName, newConfigContent, 0644); err != nil {
		logger.Logf(logger.LevelInfo, "Error writing temporary Nagios config file %s: %v", tempFileName, err)
		return
	}

	// Atomically replace the old config file with the new one
	if err := os.Rename(tempFileName, finalFileName); err != nil {
		logger.Logf(logger.LevelInfo, "Error renaming temporary Nagios config file to %s: %v", finalFileName, err)
		// Attempt to clean up temp file if rename fails
		if removeErr := os.Remove(tempFileName); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			logger.Logf(logger.LevelInfo, "Error removing temporary file %s after rename failure: %v", tempFileName, removeErr)
		}
		return
	}

	logger.Logf(logger.LevelInfo, "Successfully generated and updated Nagios config: %s (%d hosts, %d services)", finalFileName, len(hosts), len(services))

	// Signal that the config has been updated
	// Use non-blocking send in case no one is listening (though main should be)
	select {
	case g.ReloadChan <- struct{}{}:
		logger.Logf(logger.LevelDebug, "Sent reload signal on ReloadChan")
	default:
		// This case should ideally not be hit if main's goroutine is always listening.
		// It prevents blocking if the channel buffer is full (which it isn't here) or if the receiver isn't ready.
		logger.Logf(logger.LevelInfo, "ReloadChan is blocked or has no listener, unable to send reload signal immediately.")
	}

	// Clean up old .cfg files (optional, be careful)
	/*
	   if err := g.cleanupOldConfigs(finalFileName); err != nil {
	       logger.Logf(logger.LevelInfo, "Error cleaning up old Nagios config files: %v", err)
	   }
	*/
}

// cleanupOldConfigs removes .cfg files in the output directory that are not the current generated file.
// Use with caution!
func (g *Generator) cleanupOldConfigs(currentFile string) error {
	files, err := ioutil.ReadDir(g.config.OutputDir)
	if err != nil {
		return fmt.Errorf("failed to read output directory %s: %w", g.config.OutputDir, err)
	}

	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(file.Name(), ".cfg") {
			filePath := filepath.Join(g.config.OutputDir, file.Name())
			if filePath != currentFile {
				logger.Logf(logger.LevelDebug, "Removing old Nagios config file: %s", filePath)
				if err := os.Remove(filePath); err != nil {
					logger.Logf(logger.LevelInfo, "Failed to remove old config file %s: %v", filePath, err)
					// Continue trying to remove others
				}
			}
		}
	}
	return nil
}
