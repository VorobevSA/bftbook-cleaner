// Package cleaner provides functionality for cleaning CometBFT addrbook files.
package cleaner

import (
	"context"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"time"
)

const (
	addrBookBucketTypeNew  byte = 0x01
	addrBookNewBucketCount      = 256
	defaultBanTimestamp         = "0001-01-01T00:00:00Z"

	// Progress reporting interval for worker pools
	progressReportInterval = 2 * time.Second

	// Maximum read timeout for basic peer response check
	maxBasicReadTimeout = 2 * time.Second

	// Buffer size for reading peer response
	peerResponseBufferSize = 64

	// Short ID display length
	shortIDLength = 8

	// Number of concurrent operations in NodeInfo exchange (read + write)
	nodeInfoExchangeOps = 2

	// CometBFT peer ID length (hex-encoded, typically 40 characters)
	peerIDLength = 40

	// Valid port range
	minPort = 1
	maxPort = 65535

	// Manual list format: ID@IP:PORT (split by @)
	manualListPartsCount = 2

	// Percentage multiplier for progress calculation
	percentMultiplier = 100
)

// Cleaner aggregates dependencies and orchestrates the workflow.
type Cleaner struct {
	cfg Config
	log *log.Logger
	now func() time.Time
}

// New builds a Cleaner with sane defaults.
func New(cfg Config, logger *log.Logger) (*Cleaner, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = log.New(os.Stdout, "", log.LstdFlags)
	}
	return &Cleaner{
		cfg: cfg,
		log: logger,
		now: time.Now,
	}, nil
}

// Run executes the full addrbook clean-up flow.
func (c *Cleaner) Run(ctx context.Context) error {
	c.log.Printf("Starting addrbook cleaner (workers=%d timeout=%s)", c.cfg.Workers, c.cfg.Timeout)

	peers, key, err := c.collectPeers(ctx)
	if err != nil {
		return fmt.Errorf("collect peers: %w", err)
	}
	if len(peers) == 0 {
		return fmt.Errorf("no peers found in %s (and manual list)", c.cfg.InputDir)
	}

	c.log.Printf("Total unique peers found: %d", len(peers))
	addrSlice := mapsToSlice(peers)

	c.log.Printf("Checking peers availability...")
	validAddrs, err := c.checkPeers(ctx, addrSlice)
	if err != nil {
		return fmt.Errorf("check peers: %w", err)
	}
	c.log.Printf("Valid peers: %d/%d", len(validAddrs), len(addrSlice))

	filtered, err := c.enrichWithNodeInfo(ctx, validAddrs)
	if err != nil {
		return fmt.Errorf("fetch node info: %w", err)
	}
	c.log.Printf("Peers after NodeInfo filters: %d/%d", len(filtered), len(validAddrs))
	if len(filtered) == 0 {
		return fmt.Errorf("no peers remained after NodeInfo filtering")
	}

	outputBook := AddrBook{
		Key:   c.resolveOutputKey(key),
		Addrs: filtered,
	}
	c.updatePeerTimestamps(outputBook.Addrs)

	if err := c.writeAddrBook(outputBook); err != nil {
		return fmt.Errorf("write addrbook: %w", err)
	}

	c.log.Printf("Successfully created clean addrbook: %s", c.cfg.OutputFile)
	return nil
}

func (c *Cleaner) resolveOutputKey(firstKey string) string {
	if firstKey != "" {
		return firstKey
	}
	return c.generateKey()
}

func mapsToSlice(peers map[string]Addr) []Addr {
	out := make([]Addr, 0, len(peers))
	for _, addr := range peers {
		out = append(out, addr)
	}
	// Keep deterministic ordering for stable output.
	slices.SortFunc(out, func(a, b Addr) int {
		return strings.Compare(a.Addr.ID, b.Addr.ID)
	})
	return out
}
