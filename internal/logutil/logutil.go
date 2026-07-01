package logutil

import (
	"io"
	"log"
)

// DefaultLogger returns logger when present or a discard logger for optional
// logging dependencies.
func DefaultLogger(logger *log.Logger) *log.Logger {
	if logger != nil {
		return logger
	}
	return log.New(io.Discard, "", 0)
}
