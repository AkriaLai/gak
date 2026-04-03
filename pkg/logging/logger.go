// Package logging implements structured logging for the agent kernel.
//
// Features:
//   - Leveled output (Debug, Info, Warn, Error)
//   - Structured fields (key-value pairs)
//   - Event tracing (correlate logs to specific turns/tool calls)
//   - Multiple outputs (stderr, file)
//   - JSON and text formatters
package logging

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Level represents log severity.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

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
		return "UNKNOWN"
	}
}

// colorCode returns ANSI color for each level.
func (l Level) colorCode() string {
	switch l {
	case LevelDebug:
		return "\033[90m"  // Gray
	case LevelInfo:
		return "\033[36m"  // Cyan
	case LevelWarn:
		return "\033[33m"  // Yellow
	case LevelError:
		return "\033[31m"  // Red
	default:
		return "\033[0m"
	}
}

// Format controls log output format.
type Format int

const (
	FormatText Format = iota
	FormatJSON
)

// Entry is a single log entry.
type Entry struct {
	Timestamp time.Time      `json:"timestamp"`
	Level     Level          `json:"level"`
	Message   string         `json:"message"`
	Fields    map[string]any `json:"fields,omitempty"`

	// Tracing context
	Turn      int    `json:"turn,omitempty"`
	ToolName  string `json:"tool,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// Logger is the structured logger.
type Logger struct {
	mu     sync.Mutex
	level  Level
	format Format
	output io.Writer

	// Default fields attached to every entry
	defaultFields map[string]any
}

// New creates a new logger.
func New(opts ...Option) *Logger {
	l := &Logger{
		level:         LevelInfo,
		format:        FormatText,
		output:        os.Stderr,
		defaultFields: make(map[string]any),
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Option configures the logger.
type Option func(*Logger)

// WithLevel sets the minimum log level.
func WithLevel(level Level) Option {
	return func(l *Logger) {
		l.level = level
	}
}

// WithFormat sets the output format.
func WithFormat(format Format) Option {
	return func(l *Logger) {
		l.format = format
	}
}

// WithOutput sets the output writer.
func WithOutput(w io.Writer) Option {
	return func(l *Logger) {
		l.output = w
	}
}

// WithField adds a default field to all log entries.
func WithField(key string, value any) Option {
	return func(l *Logger) {
		l.defaultFields[key] = value
	}
}

// Debug logs at debug level.
func (l *Logger) Debug(msg string, fields ...any) {
	l.log(LevelDebug, msg, fields...)
}

// Info logs at info level.
func (l *Logger) Info(msg string, fields ...any) {
	l.log(LevelInfo, msg, fields...)
}

// Warn logs at warn level.
func (l *Logger) Warn(msg string, fields ...any) {
	l.log(LevelWarn, msg, fields...)
}

// Error logs at error level.
func (l *Logger) Error(msg string, fields ...any) {
	l.log(LevelError, msg, fields...)
}

// WithTurn returns a sub-logger with a turn context.
func (l *Logger) WithTurn(turn int) *TurnLogger {
	return &TurnLogger{logger: l, turn: turn}
}

// log creates and writes a log entry.
func (l *Logger) log(level Level, msg string, fields ...any) {
	if level < l.level {
		return
	}

	entry := Entry{
		Timestamp: time.Now(),
		Level:     level,
		Message:   msg,
		Fields:    make(map[string]any),
	}

	// Copy default fields
	for k, v := range l.defaultFields {
		entry.Fields[k] = v
	}

	// Parse variadic key-value pairs
	for i := 0; i+1 < len(fields); i += 2 {
		key, ok := fields[i].(string)
		if !ok {
			continue
		}
		entry.Fields[key] = fields[i+1]
	}

	l.write(entry)
}

// logEntry writes a pre-built entry.
func (l *Logger) logEntry(entry Entry) {
	if entry.Level < l.level {
		return
	}

	// Merge default fields
	for k, v := range l.defaultFields {
		if _, exists := entry.Fields[k]; !exists {
			entry.Fields[k] = v
		}
	}

	l.write(entry)
}

func (l *Logger) write(entry Entry) {
	l.mu.Lock()
	defer l.mu.Unlock()

	var line string
	switch l.format {
	case FormatJSON:
		data, _ := json.Marshal(entry)
		line = string(data) + "\n"
	default:
		line = l.formatText(entry)
	}

	fmt.Fprint(l.output, line)
}

func (l *Logger) formatText(entry Entry) string {
	ts := entry.Timestamp.Format("15:04:05.000")
	color := entry.Level.colorCode()
	reset := "\033[0m"

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s%s [%s]%s %s", color, ts, entry.Level, reset, entry.Message))

	if entry.Turn > 0 {
		sb.WriteString(fmt.Sprintf(" turn=%d", entry.Turn))
	}
	if entry.ToolName != "" {
		sb.WriteString(fmt.Sprintf(" tool=%s", entry.ToolName))
	}

	for k, v := range entry.Fields {
		sb.WriteString(fmt.Sprintf(" %s=%v", k, v))
	}

	sb.WriteString("\n")
	return sb.String()
}

// TurnLogger is a logger scoped to a specific inference turn.
type TurnLogger struct {
	logger *Logger
	turn   int
}

// Debug logs at debug level with turn context.
func (tl *TurnLogger) Debug(msg string, fields ...any) {
	tl.logWithTurn(LevelDebug, msg, fields...)
}

// Info logs at info level with turn context.
func (tl *TurnLogger) Info(msg string, fields ...any) {
	tl.logWithTurn(LevelInfo, msg, fields...)
}

// Warn logs at warn level with turn context.
func (tl *TurnLogger) Warn(msg string, fields ...any) {
	tl.logWithTurn(LevelWarn, msg, fields...)
}

// Error logs at error level with turn context.
func (tl *TurnLogger) Error(msg string, fields ...any) {
	tl.logWithTurn(LevelError, msg, fields...)
}

// Tool returns a sub-logger with both turn and tool context.
func (tl *TurnLogger) Tool(toolName string) *ToolLogger {
	return &ToolLogger{turnLogger: tl, toolName: toolName}
}

func (tl *TurnLogger) logWithTurn(level Level, msg string, fields ...any) {
	entry := Entry{
		Timestamp: time.Now(),
		Level:     level,
		Message:   msg,
		Turn:      tl.turn,
		Fields:    make(map[string]any),
	}

	for i := 0; i+1 < len(fields); i += 2 {
		key, ok := fields[i].(string)
		if !ok {
			continue
		}
		entry.Fields[key] = fields[i+1]
	}

	tl.logger.logEntry(entry)
}

// ToolLogger is a logger scoped to a specific tool call within a turn.
type ToolLogger struct {
	turnLogger *TurnLogger
	toolName   string
}

// Info logs at info level with turn + tool context.
func (tl *ToolLogger) Info(msg string, fields ...any) {
	entry := Entry{
		Timestamp: time.Now(),
		Level:     LevelInfo,
		Message:   msg,
		Turn:      tl.turnLogger.turn,
		ToolName:  tl.toolName,
		Fields:    make(map[string]any),
	}

	for i := 0; i+1 < len(fields); i += 2 {
		key, ok := fields[i].(string)
		if !ok {
			continue
		}
		entry.Fields[key] = fields[i+1]
	}

	tl.turnLogger.logger.logEntry(entry)
}

// Error logs at error level with turn + tool context.
func (tl *ToolLogger) Error(msg string, fields ...any) {
	entry := Entry{
		Timestamp: time.Now(),
		Level:     LevelError,
		Message:   msg,
		Turn:      tl.turnLogger.turn,
		ToolName:  tl.toolName,
		Fields:    make(map[string]any),
	}

	for i := 0; i+1 < len(fields); i += 2 {
		key, ok := fields[i].(string)
		if !ok {
			continue
		}
		entry.Fields[key] = fields[i+1]
	}

	tl.turnLogger.logger.logEntry(entry)
}
