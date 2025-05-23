package logutil

import (
	"log"
	"os"
	"strings"
)

var logLevel = parseLogLevel(os.Getenv("LOG_LEVEL"))

type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

func parseLogLevel(s string) Level {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "debug":
		return LevelDebug
	case "warn":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

func Debugf(format string, v ...interface{}) {
	if logLevel <= LevelDebug {
		log.Printf("[DEBUG] "+format, v...)
	}
}

func Infof(format string, v ...interface{}) {
	if logLevel <= LevelInfo {
		log.Printf("[INFO] "+format, v...)
	}
}

func Warnf(format string, v ...interface{}) {
	if logLevel <= LevelWarn {
		log.Printf("[WARN] "+format, v...)
	}
}

func Errorf(format string, v ...interface{}) {
	log.Printf("[ERROR] "+format, v...)
}
