package main

import (
	"encoding/xml"
	"flag"
	"log"
	"net/http"
	"os"
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
)

func init() {
	flag.StringVar(&configFile, "config", "", "Path to configuration file")
	flag.Parse()

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
