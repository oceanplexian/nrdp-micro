package logger

import (
	"fmt"
	"log"
	"strings"
)

// Level represents different logging levels
type Level int

const (
	LevelInfo Level = iota
	LevelDebug
	LevelTrace
)

var (
	currentLevel Level
	logger       *log.Logger
)

// Configure sets up the logger with the specified level
func Configure(l Level, lg *log.Logger) {
	currentLevel = l
	logger = lg
}

// Message represents a structured log message
type Message struct {
	Level   string      `json:"level,omitempty"`
	Event   string      `json:"event,omitempty"`
	Host    string      `json:"host,omitempty"`
	Message string      `json:"msg,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

func (m Message) String() string {
	var parts []string
	
	if m.Level != "" {
		parts = append(parts, m.Level)
	}
	if m.Event != "" {
		parts = append(parts, m.Event)
	}
	if m.Host != "" {
		parts = append(parts, fmt.Sprintf("host=%s", m.Host))
	}
	if m.Message != "" {
		parts = append(parts, m.Message)
	}
	if m.Data != nil {
		parts = append(parts, fmt.Sprintf("%v", m.Data))
	}
	
	return strings.Join(parts, " ")
}

// CurrentLevel returns the current logging level
func CurrentLevel() Level {
	return currentLevel
}

// Logf logs a message at the specified level
func Logf(level Level, format string, args ...interface{}) {
	if level <= currentLevel && logger != nil {
		logger.Printf(format, args...)
	}
}

// Info logs a message at info level
func Info(msg Message) {
	Logf(LevelInfo, "%s", msg)
}

// Debug logs a message at debug level
func Debug(msg Message) {
	Logf(LevelDebug, "%s", msg)
}

// Trace logs a message at trace level
func Trace(msg Message) {
	Logf(LevelTrace, "%s", msg)
} 