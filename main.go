package main

import (
	"encoding/xml"
	"flag"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"nrdp_micro/check"
	"nrdp_micro/config"
	"nrdp_micro/db"
	"nrdp_micro/logger"
	"nrdp_micro/metrics"
	"nrdp_micro/nagios_config"
	"nrdp_micro/storage"
)

var (
	configFile string
	cfg        *config.Config
	dbManager  *db.Manager

	// Flag to enable logging of all incoming requests
	logAllRequests bool
)

func init() {
	flag.StringVar(&configFile, "config", "", "Path to configuration file")
	flag.Parse()

	// Check DEBUG environment variable
	debugEnv := strings.ToLower(os.Getenv("DEBUG"))
	logAllRequests = (debugEnv == "1" || debugEnv == "true")

	// Configure logger first with default settings
	logger.Configure(logger.LevelInfo, log.New(os.Stdout, "", log.Ldate|log.Ltime))

	// Load configuration
	var err error
	cfg, err = config.Load(configFile)
	if err != nil {
		logger.Logf(logger.LevelInfo, "Failed to load configuration: %v", err)
		os.Exit(1)
	}

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		logger.Logf(logger.LevelInfo, "Invalid configuration: %v", err)
		os.Exit(1)
	}

	// Initialize Database Manager
	var dbErr error
	dbManager, dbErr = db.NewManager(cfg.DatabasePath)
	if dbErr != nil {
		logger.Logf(logger.LevelInfo, "Failed to initialize database: %v", dbErr)
		os.Exit(1)
	}

	// Reconfigure logger with proper level from config
	logLevel := logger.LevelInfo
	switch cfg.Logging.Level {
	case "debug":
		logLevel = logger.LevelDebug
	case "trace":
		logLevel = logger.LevelTrace
	}
	logger.Configure(logLevel, log.New(os.Stdout, "", log.Ldate|log.Ltime))

	// Start system monitoring
	monitorSystem()

	// Start Nagios config generator
	nagiosGen, err := nagios_config.NewGenerator(&cfg.Nagios, dbManager)
	if err != nil {
		logger.Logf(logger.LevelInfo, "Failed to create Nagios config generator: %v", err)
		os.Exit(1)
	}
	nagiosGen.Start()

	// Start goroutine to listen for Nagios config changes and trigger reload
	go watchNagiosConfigReload(nagiosGen.ReloadChan, cfg.Nagios.ReloadCommand)

	// Log initial storage stats
	if stats, err := storage.NewManager(
		cfg.Storage.OutputDir,
		cfg.Storage.MaxFiles,
		cfg.Storage.MinDiskSpace,
	).GetStats(); err == nil {
		logger.Logf(logger.LevelInfo, "Storage stats: %v", stats)
	}

	// Create HTTP handler with storage manager and db manager
	handler := &Handler{
		storage: storage.NewManager(
			cfg.Storage.OutputDir,
			cfg.Storage.MaxFiles,
			cfg.Storage.MinDiskSpace,
		),
		db: dbManager,
	}

	// Set up HTTP server
	http.HandleFunc("/", handler.handleRequest)
	logger.Logf(logger.LevelInfo, "Starting server on %s...", cfg.Server.ListenAddr)
	if err := http.ListenAndServe(cfg.Server.ListenAddr, nil); err != nil {
		logger.Logf(logger.LevelInfo, "Server failed: %v", err)
		os.Exit(1)
	}
}

func main() {
	// Create storage manager
	storageManager := storage.NewManager(
		cfg.Storage.OutputDir,
		cfg.Storage.MaxFiles,
		cfg.Storage.MinDiskSpace,
	)

	// Verify storage is ready
	if err := storageManager.EnsureWritable(); err != nil {
		logger.Logf(logger.LevelInfo, "Storage check failed: %v", err)
		os.Exit(1)
	}

	// Start system monitoring
	monitorSystem()

	// Start Nagios config generator
	nagiosGen, err := nagios_config.NewGenerator(&cfg.Nagios, dbManager)
	if err != nil {
		logger.Logf(logger.LevelInfo, "Failed to create Nagios config generator: %v", err)
		os.Exit(1)
	}
	nagiosGen.Start()

	// Start goroutine to listen for Nagios config changes and trigger reload
	go watchNagiosConfigReload(nagiosGen.ReloadChan, cfg.Nagios.ReloadCommand)

	// Log initial storage stats
	if stats, err := storageManager.GetStats(); err == nil {
		logger.Logf(logger.LevelInfo, "Storage stats: %v", stats)
	}

	// Create HTTP handler with storage manager and db manager
	handler := &Handler{
		storage: storageManager,
		db:      dbManager,
	}

	// Set up HTTP server
	http.HandleFunc("/", handler.handleRequest)
	logger.Logf(logger.LevelInfo, "Starting server on %s...", cfg.Server.ListenAddr)
	if err := http.ListenAndServe(cfg.Server.ListenAddr, nil); err != nil {
		logger.Logf(logger.LevelInfo, "Server failed: %v", err)
		os.Exit(1)
	}
}

type Handler struct {
	storage *storage.Manager
	db      *db.Manager
}

func (h *Handler) handleRequest(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	defer logger.Logf(logger.LevelDebug, "Handler finished for %s", r.RemoteAddr)

	// Log request details if debug flag is set
	if logAllRequests {
		logger.Logf(logger.LevelInfo, "Received request: Method=%s, URL=%s, RemoteAddr=%s, UserAgent=%s",
			r.Method, r.URL.String(), r.RemoteAddr, r.UserAgent())
	}

	if r.Method != http.MethodPost {
		logger.Logf(logger.LevelDebug, "Invalid request method: %s", r.Method)
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	// Parse the form data
	err := r.ParseForm()
	if err != nil {
		logger.Logf(logger.LevelDebug, "Failed to parse form data: %v", err)
		http.Error(w, "Failed to parse form data", http.StatusBadRequest)
		return
	}

	// Extract the XML data from the form
	xmlData := r.FormValue("XMLDATA")
	if xmlData == "" {
		logger.Logf(logger.LevelDebug, "Missing XMLDATA in request")
		http.Error(w, "Missing XMLDATA in request", http.StatusBadRequest)
		return
	}

	// Log the raw XML data if requested
	if cfg.Logging.ShowRaw {
		logger.Logf(logger.LevelDebug, "Raw XMLDATA: %s", xmlData)
	}

	// Parse the XML data
	var results check.Results
	if err := xml.Unmarshal([]byte(xmlData), &results); err != nil {
		logger.Logf(logger.LevelDebug, "Failed to parse XML data: %v", err)
		http.Error(w, "Failed to parse XML data", http.StatusBadRequest)
		return
	}

	// Log check results summary
	results.LogSummary()

	// Get current time for last_seen updates
	now := time.Now()

	// Process each check result
	processor := &check.Processor{
		OutputDir: cfg.Storage.OutputDir,
		GroupName: cfg.Storage.GroupName,
		Storage:   h.storage,
	}

	uniqueHosts := make(map[string]struct{})

	for _, result := range results.CheckResult {
		// Update host last_seen in DB
		if _, exists := uniqueHosts[result.HostName]; !exists {
			if err := h.db.UpdateHost(result.HostName, now); err != nil {
				logger.Logf(logger.LevelDebug, "Failed to update host %s in DB: %v", result.HostName, err)
			}
			uniqueHosts[result.HostName] = struct{}{}
		}

		// Update service last_seen in DB
		if result.ServiceName != "" { // Only update if service name exists
			if err := h.db.UpdateService(result.HostName, result.ServiceName, now); err != nil {
				logger.Logf(logger.LevelDebug, "Failed to update service '%s' for host %s in DB: %v", result.ServiceName, result.HostName, err)
			}
		}

		// Process the check result (write to file)
		if err := processor.Process(result); err != nil {
			logger.Logf(logger.LevelDebug, "Failed to process check result for %s - %s: %v", result.HostName, result.ServiceName, err)
			http.Error(w, "Failed to process check result", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusOK)
}

// watchNagiosConfigReload listens on the reload channel and executes the reload command.
func watchNagiosConfigReload(reloadChan <-chan struct{}, reloadCmd string) {
	if reloadCmd == "" {
		logger.Logf(logger.LevelInfo, "Nagios reload command is empty, watcher will not execute commands.")
		// Keep listening to drain the channel if necessary, but do nothing.
		for range reloadChan {
			logger.Logf(logger.LevelDebug, "Received Nagios config update signal, but no reload command configured.")
		}
		return
	}

	logger.Logf(logger.LevelInfo, "Starting Nagios reload watcher (command: '%s')", reloadCmd)
	for range reloadChan {
		logger.Logf(logger.LevelInfo, "Received Nagios config update signal. Attempting to execute reload command...")
		executeReloadCommand(reloadCmd)
	}
	logger.Logf(logger.LevelInfo, "Nagios reload watcher stopped.") // Should ideally not happen
}

// executeReloadCommand runs the configured command to reload Nagios.
func executeReloadCommand(command string) {
	logger.Logf(logger.LevelDebug, "Executing reload command: %s", command)

	// Use sh -c to handle potential pipelines or complex commands in the string
	cmd := exec.Command("sh", "-c", command)

	// Capture combined output (stdout and stderr)
	output, err := cmd.CombinedOutput()

	if err != nil {
		logger.Logf(logger.LevelInfo, "Warning: Failed to execute Nagios reload command '%s': %v. Output: %s", command, err, string(output))
		return
	}

	logger.Logf(logger.LevelInfo, "Successfully executed Nagios reload command '%s'. Output: %s", command, string(output))
}

func monitorSystem() {
	ticker := time.NewTicker(time.Second)
	go func() {
		// Log initial metrics
		currentMetrics := metrics.GetMetrics()
		if cfg.Logging.Verbose {
			logger.Logf(logger.LevelDebug, "%s", currentMetrics.DetailString())
		} else {
			logger.Logf(logger.LevelDebug, "%s", currentMetrics.String())
		}

		lastMetrics := currentMetrics
		for range ticker.C {
			currentMetrics = metrics.GetMetrics()

			// Log metrics based on verbosity and changes
			if cfg.Logging.Verbose || currentMetrics.HasSignificantChanges(lastMetrics) {
				logger.Logf(logger.LevelDebug, "%s", currentMetrics.DetailString())
			} else {
				logger.Logf(logger.LevelDebug, "%s", currentMetrics.String())
			}

			lastMetrics = currentMetrics
		}
	}()
}
