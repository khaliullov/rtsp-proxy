package rtspproxy

import (
	"log"
	"sync/atomic"
)

var verbose atomic.Bool

// SetVerbose sets the verbosity level for logging.
func SetVerbose(v bool) {
	verbose.Store(v)
}

// Logf prints a log message if verbose logging is enabled.
func Logf(format string, v ...interface{}) {
	if verbose.Load() {
		log.Printf(format, v...)
	}
}

// LogCriticalf prints a critical log message regardless of verbosity.
func LogCriticalf(format string, v ...interface{}) {
	log.Printf(format, v...)
}
