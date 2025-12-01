package cleaner

import (
	"io"
	"log"
)

func closeWithLog(logger *log.Logger, closer io.Closer, label string, verbose bool) {
	if closer == nil {
		return
	}
	if err := closer.Close(); err != nil && logger != nil && verbose {
		logger.Printf("Warning: failed to close %s: %v", label, err)
	}
}
