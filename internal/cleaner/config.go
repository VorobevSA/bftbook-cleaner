package cleaner

import (
	"errors"
	"fmt"
	"time"
)

// Config contains runtime options for the Cleaner.
type Config struct {
	InputDir      string
	OutputFile    string
	ManualList    string
	Workers       int
	Timeout       time.Duration
	FilterNetwork string
	FilterVersion string
	Verbose       bool
}

// Validate ensures config fields follow reasonable constraints.
func (c Config) Validate() error {
	if c.InputDir == "" {
		return errors.New("input directory is required")
	}
	if c.OutputFile == "" {
		return errors.New("output file is required")
	}
	if c.Workers <= 0 {
		return fmt.Errorf("workers must be positive, got %d", c.Workers)
	}
	if c.Timeout <= 0 {
		return fmt.Errorf("timeout must be positive, got %s", c.Timeout)
	}
	return nil
}
