package cleaner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (c *Cleaner) collectPeers(ctx context.Context) (map[string]Addr, string, error) {
	allAddrs := make(map[string]Addr)
	var firstKey string
	firstKeySet := false

	jsonFiles, err := c.findJSONFiles()
	if err != nil {
		c.log.Printf("Warning: cannot read directory %s: %v", c.cfg.InputDir, err)
	} else if len(jsonFiles) > 0 {
		c.log.Printf("Found %d JSON files to process", len(jsonFiles))
		for _, file := range jsonFiles {
			select {
			case <-ctx.Done():
				return nil, "", ctx.Err()
			default:
			}

			c.log.Printf("Reading file: %s", file)
			addrBook, addrs, err := c.readAddrBook(file)
			if err != nil {
				c.log.Printf("Warning: failed to read %s: %v", file, err)
				continue
			}

			if !firstKeySet && addrBook.Key != "" {
				firstKey = addrBook.Key
				firstKeySet = true
			}

			for _, addr := range addrs {
				allAddrs[addr.Addr.ID] = addr
			}
		}
	}

	if c.cfg.ManualList != "" {
		c.log.Printf("Reading manual list: %s", c.cfg.ManualList)
		manualAddrs, err := c.readManualList(c.cfg.ManualList)
		if err != nil {
			c.log.Printf("Warning: failed to read manual list %s: %v", c.cfg.ManualList, err)
		} else {
			c.log.Printf("Found %d peers in manual list", len(manualAddrs))
			for _, addr := range manualAddrs {
				allAddrs[addr.Addr.ID] = addr
			}
		}
	}

	return allAddrs, firstKey, nil
}

func (c *Cleaner) findJSONFiles() ([]string, error) {
	entries, err := os.ReadDir(c.cfg.InputDir)
	if err != nil {
		return nil, err
	}

	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if filepath.Ext(entry.Name()) == ".json" {
			files = append(files, filepath.Join(c.cfg.InputDir, entry.Name()))
		}
	}

	return files, nil
}

func (c *Cleaner) readAddrBook(path string) (*AddrBook, []Addr, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer closeWithLog(c.log, file, path)

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, nil, err
	}

	var addrBook AddrBook
	if err := json.Unmarshal(data, &addrBook); err != nil {
		return nil, nil, fmt.Errorf("parse json: %w", err)
	}

	return &addrBook, addrBook.Addrs, nil
}

func (c *Cleaner) readManualList(path string) ([]Addr, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer closeWithLog(c.log, file, path)

	var addrs []Addr
	scanner := bufio.NewScanner(file)
	lineNum := 0
	defaultTimestamp := c.now().UTC().Format(time.RFC3339)

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		addr, err := c.parseManualLine(line)
		if err != nil {
			c.log.Printf("Warning: %s line %d: %v", path, lineNum, err)
			continue
		}

		addr.LastAttempt = defaultTimestamp
		addr.LastSuccess = defaultTimestamp
		addr.LastBanTime = defaultBanTimestamp
		addrs = append(addrs, addr)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read manual list: %w", err)
	}

	return addrs, nil
}

func (c *Cleaner) parseManualLine(line string) (Addr, error) {
	parts := strings.Split(line, "@")
	if len(parts) != 2 {
		return Addr{}, errors.New("invalid format, expected ID@IP:PORT")
	}

	id := strings.TrimSpace(parts[0])
	addrPart := strings.TrimSpace(parts[1])

	host, portStr, err := net.SplitHostPort(addrPart)
	if err != nil {
		return Addr{}, fmt.Errorf("invalid address: %w", err)
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return Addr{}, fmt.Errorf("invalid port: %w", err)
	}

	addr := Addr{
		Addr: Address{
			ID:   id,
			IP:   host,
			Port: port,
		},
		Src: Address{
			ID:   id,
			IP:   host,
			Port: port,
		},
		Buckets:    []int{manualBucketIndex(id)},
		Attempts:   0,
		BucketType: int(addrBookBucketTypeNew),
	}

	return addr, nil
}

func manualBucketIndex(id string) int {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(id))
	return int(hasher.Sum32() % uint32(addrBookNewBucketCount))
}
