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

	cmtbytes "github.com/cometbft/cometbft/libs/bytes"
	"github.com/cometbft/cometbft/libs/protoio"
	tmp2p "github.com/cometbft/cometbft/proto/tendermint/p2p"
	"github.com/cometbft/cometbft/version"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/p2p"
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
	inputDir      = flag.String("input", "input", "Directory containing input JSON files")
	outputFile    = flag.String("output", "output.addrbook.json", "Output file path")
	workers       = flag.Int("workers", 50, "Number of concurrent workers for peer checking")
	timeout       = flag.Duration("timeout", 5*time.Second, "Timeout for peer connection and NodeInfo requests")
	filterNetwork = flag.String("network", "", "Filter peers by NodeInfo network (optional)")
	filterVersion = flag.String("version", "", "Filter peers by NodeInfo version (optional)")
	verbose       = flag.Bool("verbose", false, "Enable verbose logging")
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

	// Fetch NodeInfo details, log them, and apply optional filters
	finalAddrs := logNodeInfos(validAddrs, *timeout, *filterNetwork, *filterVersion)
	log.Printf("Peers after NodeInfo filters: %d out of %d", len(finalAddrs), len(validAddrs))

	// Use key from first file or generate new one
	outputKey := firstKey
	if outputKey == "" {
		outputKey = generateKey()
	}

	// Create output addrbook
	outputBook := AddrBook{
		Key:   outputKey,
		Addrs: finalAddrs,
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

type nodeInfoResult struct {
	addr Addr
	info *p2p.DefaultNodeInfo
	err  error
}

// logNodeInfos dials every valid peer, retrieves NodeInfo via the P2P handshake, logs it, and applies optional filters.
func logNodeInfos(addrs []Addr, timeout time.Duration, networkFilter, versionFilter string) []Addr {
	if len(addrs) == 0 {
		return nil
	}

	workerCount := *workers
	if workerCount <= 0 {
		workerCount = 1
	}

	if *verbose {
		log.Printf("Fetching NodeInfo for %d peers using %d workers...", len(addrs), workerCount)
	}

	addrChan := make(chan Addr, len(addrs))
	resultChan := make(chan nodeInfoResult, len(addrs))

	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for addr := range addrChan {
				info, err := fetchNodeInfo(addr.Addr.IP, addr.Addr.Port, timeout)
				resultChan <- nodeInfoResult{
					addr: addr,
					info: info,
					err:  err,
				}
			}
		}()
	}

	go func() {
		for _, addr := range addrs {
			addrChan <- addr
		}
		close(addrChan)
	}()

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	filtered := make([]Addr, 0, len(addrs))
	for result := range resultChan {
		if result.err != nil {
			if *verbose {
				log.Printf(
					"NodeInfo: %s:%d (ID: %s) error: %v",
					result.addr.Addr.IP,
					result.addr.Addr.Port,
					shortID(result.addr.Addr.ID),
					result.err,
				)
			}
			continue
		}

		info := result.info
		if *verbose {
			log.Printf(
				"NodeInfo: %s:%d (ID: %s) moniker=%s network=%s version=%s proto[p2p=%d block=%d app=%d] listen=%s tx_index=%s rpc=%s",
				result.addr.Addr.IP,
				result.addr.Addr.Port,
				shortID(result.addr.Addr.ID),
				info.Moniker,
				info.Network,
				info.Version,
				info.ProtocolVersion.P2P,
				info.ProtocolVersion.Block,
				info.ProtocolVersion.App,
				info.ListenAddr,
				info.Other.TxIndex,
				info.Other.RPCAddress,
			)
		}

		if networkFilter != "" && info.Network != networkFilter {
			if *verbose {
				log.Printf("NodeInfo filter: dropping %s (network %s != %s)", shortID(result.addr.Addr.ID), info.Network, networkFilter)
			}
			continue
		}
		if versionFilter != "" && info.Version != versionFilter {
			if *verbose {
				log.Printf("NodeInfo filter: dropping %s (version %s != %s)", shortID(result.addr.Addr.ID), info.Version, versionFilter)
			}
			continue
		}

		filtered = append(filtered, result.addr)
	}

	return filtered
}

// fetchNodeInfo establishes a short-lived P2P session and retrieves the peer's NodeInfo.
func fetchNodeInfo(ip string, port int, timeout time.Duration) (*p2p.DefaultNodeInfo, error) {
	address := net.JoinHostPort(ip, fmt.Sprintf("%d", port))
	tcpConn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return nil, fmt.Errorf("dial failed: %w", err)
	}
	defer tcpConn.Close()

	privKey := ed25519.GenPrivKey()
	secretConn, err := conn.MakeSecretConnection(tcpConn, privKey)
	if err != nil {
		return nil, fmt.Errorf("secret connection failed: %w", err)
	}
	defer secretConn.Close()

	localInfo := buildLocalNodeInfo(privKey)
	peerInfo, err := exchangeNodeInfo(secretConn, timeout, localInfo)
	if err != nil {
		return nil, err
	}

	return &peerInfo, nil
}

// exchangeNodeInfo replicates the CometBFT handshake to read peer NodeInfo.
func exchangeNodeInfo(c net.Conn, timeout time.Duration, ourInfo p2p.DefaultNodeInfo) (p2p.DefaultNodeInfo, error) {
	if err := c.SetDeadline(time.Now().Add(timeout)); err != nil {
		return p2p.DefaultNodeInfo{}, err
	}

	errc := make(chan error, 2)
	var remote tmp2p.DefaultNodeInfo

	go func() {
		_, err := protoio.NewDelimitedWriter(c).WriteMsg(ourInfo.ToProto())
		errc <- err
	}()

	go func() {
		reader := protoio.NewDelimitedReader(c, p2p.MaxNodeInfoSize())
		_, err := reader.ReadMsg(&remote)
		errc <- err
	}()

	for i := 0; i < cap(errc); i++ {
		if err := <-errc; err != nil {
			return p2p.DefaultNodeInfo{}, err
		}
	}

	if err := c.SetDeadline(time.Time{}); err != nil {
		return p2p.DefaultNodeInfo{}, err
	}

	info, err := p2p.DefaultNodeInfoFromToProto(&remote)
	if err != nil {
		return p2p.DefaultNodeInfo{}, err
	}

	return info, nil
}

// buildLocalNodeInfo creates a minimal NodeInfo that passes validation for handshake purposes.
func buildLocalNodeInfo(privKey ed25519.PrivKey) p2p.DefaultNodeInfo {
	return p2p.DefaultNodeInfo{
		ProtocolVersion: p2p.NewProtocolVersion(version.P2PProtocol, version.BlockProtocol, 0),
		DefaultNodeID:   p2p.PubKeyToID(privKey.PubKey()),
		ListenAddr:      "0.0.0.0:0",
		Network:         "",
		Version:         version.TMCoreSemVer,
		Channels:        cmtbytes.HexBytes{},
		Moniker:         "addrbook-cleaner",
		Other: p2p.DefaultNodeInfoOther{
			TxIndex:    "off",
			RPCAddress: "",
		},
	}
}

// shortID returns a shortened peer ID for cleaner log lines.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
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
