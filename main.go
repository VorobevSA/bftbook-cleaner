package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/p2p/conn"
)

// AddrBook represents the structure of CometBFT addrbook.json
type AddrBook struct {
	Key   string `json:"key"`
	Addrs []Addr `json:"addrs"`
}

// Addr represents a single address entry in the addrbook
type Addr struct {
	Addr        Address `json:"addr"`
	Src         Address `json:"src"`
	Buckets     []int   `json:"buckets"`
	Attempts    int     `json:"attempts"`
	BucketType  int     `json:"bucket_type"`
	LastAttempt string  `json:"last_attempt"`
	LastSuccess string  `json:"last_success"`
	LastBanTime string  `json:"last_ban_time"`
}

// Address represents IP and port information
type Address struct {
	ID   string `json:"id"`
	IP   string `json:"ip"`
	Port int    `json:"port"`
}

// PeerCheckResult contains the result of checking a peer
type PeerCheckResult struct {
	Addr  Addr
	Valid bool
}

var (
	inputDir   = flag.String("input", "input", "Directory containing input JSON files")
	outputFile = flag.String("output", "output.addrbook.json", "Output file path")
	workers    = flag.Int("workers", 50, "Number of concurrent workers for peer checking")
	timeout    = flag.Duration("timeout", 5*time.Second, "Timeout for peer connection check")
	verbose    = flag.Bool("verbose", false, "Enable verbose logging")
)

func main() {
	flag.Parse()

	log.Printf("Starting addrbook cleaner...")
	log.Printf("Input directory: %s", *inputDir)
	log.Printf("Output file: %s", *outputFile)
	log.Printf("Workers: %d", *workers)
	log.Printf("Timeout: %s", *timeout)

	// Read all JSON files from input directory
	jsonFiles, err := findJSONFiles(*inputDir)
	if err != nil {
		log.Fatalf("Error finding JSON files: %v", err)
	}

	if len(jsonFiles) == 0 {
		log.Fatalf("No JSON files found in directory: %s", *inputDir)
	}

	log.Printf("Found %d JSON files to process", len(jsonFiles))

	// Read and parse all addrbooks
	allAddrs := make(map[string]Addr) // Use map to deduplicate by peer ID
	var firstKey string
	firstKeySet := false

	for _, file := range jsonFiles {
		log.Printf("Reading file: %s", file)
		addrBook, addrs, err := readAddrBook(file)
		if err != nil {
			log.Printf("Warning: failed to read %s: %v", file, err)
			continue
		}

		// Save key from first file
		if !firstKeySet && addrBook.Key != "" {
			firstKey = addrBook.Key
			firstKeySet = true
		}

		for _, addr := range addrs {
			// Use peer ID as key to avoid duplicates
			allAddrs[addr.Addr.ID] = addr
		}
	}

	log.Printf("Total unique peers found: %d", len(allAddrs))

	// Convert map to slice for processing
	addrSlice := make([]Addr, 0, len(allAddrs))
	for _, addr := range allAddrs {
		addrSlice = append(addrSlice, addr)
	}

	// Check peers concurrently
	log.Printf("Checking peers availability...")
	validAddrs := checkPeers(addrSlice, *workers, *timeout)

	log.Printf("Valid peers: %d out of %d", len(validAddrs), len(addrSlice))

	// Use key from first file or generate new one
	outputKey := firstKey
	if outputKey == "" {
		outputKey = generateKey()
	}

	// Create output addrbook
	outputBook := AddrBook{
		Key:   outputKey,
		Addrs: validAddrs,
	}

	// Write output file
	if err := writeAddrBook(*outputFile, outputBook); err != nil {
		log.Fatalf("Error writing output file: %v", err)
	}

	log.Printf("Successfully created clean addrbook: %s", *outputFile)
}

// findJSONFiles finds all JSON files in the specified directory
func findJSONFiles(dir string) ([]string, error) {
	var files []string

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}

	return files, nil
}

// readAddrBook reads and parses an addrbook JSON file
func readAddrBook(filepath string) (*AddrBook, []Addr, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, nil, err
	}

	var addrBook AddrBook
	if err := json.Unmarshal(data, &addrBook); err != nil {
		return nil, nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	return &addrBook, addrBook.Addrs, nil
}

// checkPeers checks peer availability concurrently using workers
func checkPeers(addrs []Addr, numWorkers int, timeout time.Duration) []Addr {
	// Create channels for work distribution
	addrChan := make(chan Addr, len(addrs))
	resultChan := make(chan PeerCheckResult, len(addrs))

	// Progress tracking
	var mu sync.Mutex
	checked := 0
	total := len(addrs)

	// Start progress reporter
	stopProgress := make(chan bool)
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				mu.Lock()
				current := checked
				mu.Unlock()
				log.Printf("Progress: %d/%d peers checked (%.1f%%)", current, total, float64(current)/float64(total)*100)
			case <-stopProgress:
				return
			}
		}
	}()

	// Start workers
	var wg sync.WaitGroup
	verboseFlag := *verbose // Capture flag value to avoid race condition
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for addr := range addrChan {
				valid := checkPeer(addr.Addr.IP, addr.Addr.Port, timeout)
				resultChan <- PeerCheckResult{
					Addr:  addr,
					Valid: valid,
				}

				mu.Lock()
				checked++
				mu.Unlock()

				if verboseFlag {
					status := "✓"
					if !valid {
						status = "✗"
					}
					log.Printf("%s Checking %s:%d (ID: %s) - Port+Handshake", status, addr.Addr.IP, addr.Addr.Port, addr.Addr.ID[:8])
				}
			}
		}()
	}

	// Send all addresses to workers
	go func() {
		defer close(addrChan)
		for _, addr := range addrs {
			addrChan <- addr
		}
	}()

	// Wait for all workers to finish and close result channel
	go func() {
		wg.Wait()
		close(resultChan)
		stopProgress <- true
	}()

	// Collect valid results
	var validAddrs []Addr
	for result := range resultChan {
		if result.Valid {
			validAddrs = append(validAddrs, result.Addr)
		}
	}

	return validAddrs
}

// checkPeer checks if a peer is reachable by attempting a TCP connection
// and then performs a P2P handshake check if port is open
func checkPeer(ip string, port int, timeout time.Duration) bool {
	// Use net.JoinHostPort to properly handle IPv6 addresses
	address := net.JoinHostPort(ip, fmt.Sprintf("%d", port))

	// Step 1: Check if port is open
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return false
	}
	defer conn.Close()

	// Step 2: If port is open, try to perform basic P2P handshake check
	return checkP2PHandshake(conn, timeout)
}

// checkP2PHandshake performs a P2P handshake check using CometBFT library
// It verifies that the peer is a valid CometBFT P2P node by attempting
// to establish a secret connection handshake
func checkP2PHandshake(connection net.Conn, timeout time.Duration) bool {
	// Set connection deadline
	connection.SetDeadline(time.Now().Add(timeout / 2))

	// Generate a temporary key pair for handshake
	// We don't need to persist this key, it's just for verification
	privKey := ed25519.GenPrivKey()

	// Try to establish a secret connection (handshake)
	// This will verify that the peer is a valid CometBFT node
	// MakeSecretConnection is a function from p2p/conn package
	secretConn, err := conn.MakeSecretConnection(connection, privKey)
	if err != nil {
		// If handshake fails, the peer might not be a valid CometBFT node
		// or might be behind a firewall/NAT that doesn't allow full handshake
		// Fall back to checking if peer sends any data
		return checkPeerBasicResponse(connection, timeout)
	}
	defer secretConn.Close()

	// If we successfully established secret connection, peer is valid
	return true
}

// checkPeerBasicResponse performs a basic check to see if peer responds
// This is a fallback when full handshake fails but peer might still be valid
// If peer doesn't send any data, it's considered invalid
func checkPeerBasicResponse(conn net.Conn, timeout time.Duration) bool {
	// Set read deadline
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	// Try to read any response from the peer
	// CometBFT nodes typically send some data upon connection
	buffer := make([]byte, 64)
	n, _ := conn.Read(buffer)

	// If we read something, peer is responding and is valid
	if n > 0 {
		return true
	}

	// If reading fails (timeout or error), peer is considered invalid
	// We don't check write capability as it's not a reliable indicator
	return false
}

// generateKey generates a key for the output addrbook
// In a real scenario, you might want to preserve the key from one of the input files
func generateKey() string {
	// Generate a simple key based on current time
	return fmt.Sprintf("%x", time.Now().UnixNano())
}

// writeAddrBook writes the addrbook to a JSON file
func writeAddrBook(filepath string, addrBook AddrBook) error {
	file, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "\t")
	return encoder.Encode(addrBook)
}
