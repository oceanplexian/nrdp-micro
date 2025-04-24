package check

import (
	"encoding/xml"
	"fmt"
	"math/rand"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"nrdp_micro/logger"
	"nrdp_micro/storage"
)

// Result represents a single check result
type Result struct {
	XMLName     xml.Name `xml:"checkresult"`
	HostName    string   `xml:"hostname"`
	ServiceName string   `xml:"servicename"`
	State       int      `xml:"state"`
	Output      string   `xml:"output"`
	Time        int64    `xml:"time"`
}

// Results represents a collection of check results
type Results struct {
	XMLName     xml.Name `xml:"checkresults"`
	CheckResult []Result `xml:"checkresult"`
}

// StateLabel returns the string representation of a check state
func StateLabel(state int) string {
	switch state {
	case 0:
		return "OK"
	case 1:
		return "WARNING"
	case 2:
		return "CRITICAL"
	case 3:
		return "UNKNOWN"
	default:
		return fmt.Sprintf("STATE_%d", state)
	}
}

// LogSummary logs a summary of the check results
func (r Results) LogSummary() {
	if len(r.CheckResult) == 0 {
		return
	}

	states := make(map[int]int)
	for _, result := range r.CheckResult {
		states[result.State]++
	}

	var stateParts []string
	for state := 0; state <= 3; state++ {
		if count := states[state]; count > 0 {
			stateParts = append(stateParts, fmt.Sprintf("%s=%d", StateLabel(state), count))
		}
	}

	msg := logger.Message{
		Event: "checks_received",
		Host:  r.CheckResult[0].HostName,
		Data: map[string]interface{}{
			"total":  len(r.CheckResult),
			"states": strings.Join(stateParts, ","),
		},
	}

	// Add non-OK services details at debug level
	if logger.CurrentLevel() > logger.LevelInfo {
		var nonOkServices []map[string]string
		for _, result := range r.CheckResult {
			if result.State != 0 {
				output := result.Output
				if len(output) > 50 {
					output = output[:47] + "..."
				}
				nonOkServices = append(nonOkServices, map[string]string{
					"service": result.ServiceName,
					"state":   StateLabel(result.State),
					"output":  output,
				})
			}
		}
		if len(nonOkServices) > 0 {
			msg.Data = map[string]interface{}{
				"total":  len(r.CheckResult),
				"states": strings.Join(stateParts, ","),
				"non_ok": nonOkServices,
			}
		}
	}

	logger.Info(msg)
}

// Processor handles processing and saving check results
type Processor struct {
	OutputDir string
	GroupName string
	Storage   *storage.Manager
}

// Process processes a single check result
func (p *Processor) Process(result Result) error {
	// Check disk space
	if err := p.Storage.CheckSpace(); err != nil {
		return fmt.Errorf("storage check failed: %v", err)
	}

	// Check and wait if too many files
	for {
		tooMany, err := p.Storage.CheckFiles()
		if err != nil {
			return fmt.Errorf("file count check failed: %v", err)
		}
		if !tooMany {
			break
		}
		logger.Logf(logger.LevelDebug, "waiting for files to be processed...")
		time.Sleep(time.Second * 10)
	}

	filename, err := p.generateFilename()
	if err != nil {
		return fmt.Errorf("failed to generate filename: %v", err)
	}

	filePath := filepath.Join(p.OutputDir, filename)
	checkResultData := p.convertToNagiosFormat(result)

	// Write the check result file
	if err := os.WriteFile(filePath, []byte(checkResultData), 0770); err != nil {
		return fmt.Errorf("failed to write check result file %s: %v", filePath, err)
	}

	// Set the group on the check result file
	if err := p.setFileGroup(filePath); err != nil {
		os.Remove(filePath)
		return fmt.Errorf("failed to set group on file %s: %v", filePath, err)
	}

	// Create the .ok file
	okFileName := filePath + ".ok"
	if err := os.WriteFile(okFileName, []byte{}, 0770); err != nil {
		os.Remove(filePath)
		return fmt.Errorf("failed to create .ok file %s: %v", okFileName, err)
	}

	// Set the group on the .ok file
	if err := p.setFileGroup(okFileName); err != nil {
		os.Remove(filePath)
		os.Remove(okFileName)
		return fmt.Errorf("failed to set group on .ok file %s: %v", okFileName, err)
	}

	logger.Trace(logger.Message{
		Event: "check_saved",
		Data: map[string]string{
			"file": filename,
			"ok":   okFileName,
		},
	})

	return nil
}

func (p *Processor) generateFilename() (string, error) {
	rand.Seed(time.Now().UnixNano())
	num := rand.Intn(1000000)
	return fmt.Sprintf("c%06d", num), nil
}

func (p *Processor) setFileGroup(fileName string) error {
	group, err := user.LookupGroup(p.GroupName)
	if err != nil {
		return fmt.Errorf("failed to look up group: %v", err)
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return fmt.Errorf("invalid GID: %v", err)
	}
	if err := os.Chown(fileName, -1, gid); err != nil {
		return fmt.Errorf("failed to change file group: %v", err)
	}
	return nil
}

func (p *Processor) convertToNagiosFormat(result Result) string {
	serviceLine := ""
	if result.ServiceName != "" {
		serviceLine = fmt.Sprintf("service_description=%s\n", result.ServiceName)
	}

	return fmt.Sprintf(
		"### NRDP Check ###\n"+
			"start_time=%d.0\n"+
			"# Time: %s\n"+
			"host_name=%s\n"+
			"%s"+
			"check_type=1\n"+
			"early_timeout=1\n"+
			"exited_ok=1\n"+
			"return_code=%d\n"+
			"output=%s\\n\n",
		result.Time,
		time.Unix(result.Time, 0).Format(time.RFC1123Z),
		result.HostName,
		serviceLine,
		result.State,
		strings.ReplaceAll(result.Output, "\n", "\\n"),
	)
} 