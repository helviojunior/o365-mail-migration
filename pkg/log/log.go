// Package log is a thin wrapper around charmbracelet/log so the rest of the
// code depends on a stable, small logging surface (same idea as enumdns).
package log

import (
	"os"

	charm "github.com/charmbracelet/log"
)

// Logger is the underlying charmbracelet logger. It also implements
// slog.Handler, so callers that need an *slog.Logger can do
// slog.New(log.Logger).
var Logger = charm.NewWithOptions(os.Stderr, charm.Options{
	ReportTimestamp: false,
	Level:           charm.WarnLevel,
})

// EnableDebug lowers the level so Debug messages are emitted.
func EnableDebug() { Logger.SetLevel(charm.DebugLevel) }

// EnableSilence raises the level above everything we emit.
func EnableSilence() { Logger.SetLevel(charm.FatalLevel + 1) }

func Debug(msg string, kv ...interface{}) { Logger.Debug(msg, kv...) }
func Info(msg string, kv ...interface{})  { Logger.Info(msg, kv...) }
func Warn(msg string, kv ...interface{})  { Logger.Warn(msg, kv...) }
func Error(msg string, kv ...interface{}) { Logger.Error(msg, kv...) }

func Debugf(format string, args ...interface{}) { Logger.Debugf(format, args...) }
func Infof(format string, args ...interface{})  { Logger.Infof(format, args...) }
func Warnf(format string, args ...interface{})  { Logger.Warnf(format, args...) }
func Errorf(format string, args ...interface{}) { Logger.Errorf(format, args...) }
