package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Level represents the minimum severity that will be emitted.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// Config controls logger initialization.
type Config struct {
	Level      string
	FilePath   string
	Output     io.Writer
	AlsoStderr bool
	Flags      int
}

// Logger is a lightweight named logger that shares the package backend.
type Logger struct {
	component string
}

var (
	mu           sync.RWMutex
	currentLevel = LevelInfo
	currentFlags = defaultFlags()
	baseLogger   = log.New(os.Stderr, "", currentFlags)
	logFile      *os.File
)

// Init initializes the package logger with package defaults.
func Init() error {
	return InitWithConfig(Config{})
}

// InitWithConfig initializes the logger with explicit config.
func InitWithConfig(cfg Config) error {
	mu.Lock()
	defer mu.Unlock()

	level := currentLevel
	if cfg.Level != "" {
		level = parseLevel(cfg.Level)
	}

	flags := cfg.Flags
	if flags == 0 {
		flags = defaultFlags()
	}

	output := cfg.Output
	if output == nil {
		output = os.Stderr
	}

	filePath := cfg.FilePath
	oldFile := logFile
	newFile := (*os.File)(nil)

	if filePath != "" {
		if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
			return fmt.Errorf("create log directory for %q: %w", filePath, err)
		}
		file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return fmt.Errorf("open log file %q: %w", filePath, err)
		}
		newFile = file

		if cfg.Output != nil {
			output = io.MultiWriter(cfg.Output, file)
		} else if cfg.AlsoStderr {
			output = io.MultiWriter(os.Stderr, file)
		} else {
			output = file
		}
	}

	if oldFile != nil {
		_ = oldFile.Close()
	}
	logFile = newFile
	currentLevel = level
	currentFlags = flags
	baseLogger.SetFlags(flags)
	baseLogger.SetOutput(output)

	return nil
}

// New returns a named logger that prefixes each entry with a component label.
func New(component string) *Logger {
	return &Logger{component: strings.TrimSpace(component)}
}

// SetLevel updates the global log level at runtime.
func SetLevel(level string) {
	mu.Lock()
	defer mu.Unlock()
	currentLevel = parseLevel(level)
}

// LevelEnabled reports whether a severity will be emitted.
func LevelEnabled(level Level) bool {
	mu.RLock()
	defer mu.RUnlock()
	return level >= currentLevel
}

// Close closes the underlying log file, if one is open.
func Close() error {
	mu.Lock()
	defer mu.Unlock()

	if logFile == nil {
		return nil
	}

	err := logFile.Close()
	logFile = nil
	baseLogger.SetOutput(os.Stderr)
	return err
}

// Debugf logs a debug message.
func Debugf(format string, v ...interface{}) {
	logf(LevelDebug, "", format, v...)
}

// Infof logs an informational message.
func Infof(format string, v ...interface{}) {
	logf(LevelInfo, "", format, v...)
}

// Warnf logs a warning message.
func Warnf(format string, v ...interface{}) {
	logf(LevelWarn, "", format, v...)
}

// Errorf logs an error message.
func Errorf(format string, v ...interface{}) {
	logf(LevelError, "", format, v...)
}

// LogInfo preserves compatibility with the original helper.
func LogInfo(format string, v ...interface{}) {
	Infof(format, v...)
}

// LogError preserves compatibility with the original helper.
func LogError(format string, v ...interface{}) {
	Errorf(format, v...)
}

// Debugf logs a debug message with a component prefix.
func (l *Logger) Debugf(format string, v ...interface{}) {
	logf(LevelDebug, l.component, format, v...)
}

// Infof logs an info message with a component prefix.
func (l *Logger) Infof(format string, v ...interface{}) {
	logf(LevelInfo, l.component, format, v...)
}

// Warnf logs a warning message with a component prefix.
func (l *Logger) Warnf(format string, v ...interface{}) {
	logf(LevelWarn, l.component, format, v...)
}

// Errorf logs an error message with a component prefix.
func (l *Logger) Errorf(format string, v ...interface{}) {
	logf(LevelError, l.component, format, v...)
}

func logf(level Level, component, format string, v ...interface{}) {
	mu.RLock()
	logger := baseLogger
	minLevel := currentLevel
	mu.RUnlock()

	if level < minLevel {
		return
	}

	message := fmt.Sprintf(format, v...)
	prefix := "[" + level.String() + "]"
	if component != "" {
		prefix += " [" + component + "]"
	}

	// Skip Output/logf/<public helper> so the reported file:line points at the caller.
	_ = logger.Output(3, prefix+" "+message)
}

func parseLevel(level string) Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return LevelDebug
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	case "info", "":
		return LevelInfo
	default:
		return LevelInfo
	}
}

func defaultFlags() int {
	return log.LstdFlags | log.Lmicroseconds | log.Lshortfile
}

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "INFO"
	}
}
