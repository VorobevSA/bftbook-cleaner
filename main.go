// Package main provides the command-line interface for the BFTbook Cleaner tool.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"bftbook-cleaner/internal/cleaner"
)

const (
	defaultWorkers        = 50
	defaultTimeoutSeconds = 5
)

func main() {
	cfg := cleaner.Config{}
	flag.StringVar(&cfg.InputDir, "input", "input", "Directory containing input JSON files")
	flag.StringVar(&cfg.OutputFile, "output", "output.addrbook.json", "Output file path")
	flag.StringVar(&cfg.ManualList, "manual-list", "", "Path to manual list file with peers in format ID@IP:PORT (one per line)")

	flag.IntVar(&cfg.Workers, "workers", defaultWorkers, "Number of concurrent workers for peer checking")
	flag.DurationVar(&cfg.Timeout, "timeout", defaultTimeoutSeconds*time.Second, "Timeout for peer connection and NodeInfo requests")
	flag.StringVar(&cfg.FilterNetwork, "network", "", "Filter peers by NodeInfo network (optional)")
	flag.StringVar(&cfg.FilterVersion, "version", "", "Filter peers by NodeInfo version (optional)")
	flag.BoolVar(&cfg.Verbose, "verbose", false, "Enable verbose logging")
	flag.Parse()

	logger := log.New(os.Stdout, "", log.LstdFlags)
	if err := cfg.Validate(); err != nil {
		logger.Fatalf("invalid configuration: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	c, err := cleaner.New(cfg, logger)
	if err != nil {
		logger.Fatalf("failed to init cleaner: %v", err)
	}

	if err := c.Run(ctx); err != nil {
		logger.Fatalf("cleaning failed: %v", err)
	}
}
