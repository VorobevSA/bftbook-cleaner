# CometBFT Addrbook Cleaner

Tool for cleaning CometBFT addrbook files from non-working peers. The program checks the availability of all peers from addrbook files and creates a clean file with only working peers.

## Features

- Reads all JSON files from the specified directory
- Merges entries from multiple files with deduplication by peer ID
- Two-stage peer availability check:
  1. Port availability check (TCP connection)
  2. P2P Handshake check using CometBFT library
- Multi-threaded checking with configurable number of workers
- Fallback mechanism: if full handshake fails, a basic peer response check is performed
- For every reachable peer, performs a CometBFT handshake to retrieve and log `NodeInfo`
- Optional filters by `NodeInfo.network` and `NodeInfo.version`
- Saves the result to a new clean addrbook file

## Installation

```bash
go mod download
```

## Usage

```bash
go run main.go [options]
```

### Parameters

- `-input` - Directory containing input JSON files (default: `input`)
- `-output` - Output file path (default: `output.addrbook.json`)
- `-workers` - Number of concurrent workers for peer checking (default: 50)
- `-timeout` - Timeout for peer connection check and NodeInfo retrieval (default: 5s)
- `-network` - Keep only peers that report this NodeInfo network (optional)
- `-version` - Keep only peers that report this NodeInfo version (optional)
- `-verbose` - Enable verbose logging (default: false)

### Examples

```bash
# Basic usage
go run main.go

# With specified directory and number of workers
go run main.go -input input -output clean.addrbook.json -workers 100

# With verbose output
go run main.go -verbose

# With increased timeout
go run main.go -timeout 10s -workers 30

# With NodeInfo-based filtering
go run main.go -network haqq_11235-1 -version 0.38.19 -timeout 8s
```

## Building

```bash
go build -o addrbook-cleaner main.go
```

After building, you can run:

```bash
./addrbook-cleaner -input input -output output.addrbook.json -workers 50
```

## How it works

1. The program scans the specified directory and finds all `.json` files
2. Reads and parses each addrbook file
3. Merges all entries, removing duplicates by peer ID
4. For each peer, performs a two-stage check:
   - **Stage 1**: Port availability check via TCP connection. If the port is unavailable, the peer is considered invalid.
   - **Stage 2**: If the port is open, P2P Handshake check is performed using CometBFT library:
     - Attempts to establish an encrypted connection (SecretConnection) via `conn.MakeSecretConnection`
     - If handshake is successful, the peer is considered a valid CometBFT node
     - **Fallback mechanism**: If full handshake fails (e.g., due to NAT/firewall or version incompatibility), a basic check is performed:
       * Attempts to read data from the peer (CometBFT nodes typically send data upon connection)
       * If reading is successful (data received), the peer is considered valid
       * If reading fails, the peer is rejected as invalid
5. For each working peer, performs a short handshake to fetch `NodeInfo`, logs it, and applies optional network/version filters
6. Saves only peers that passed all checks (including optional filters) to the output file

## Output file structure

The output file has the same structure as the input addrbook files:
- `key` - unique addrbook key
- `addrs` - array of addresses with peer information

Each entry contains:
- `addr` - peer address (ID, IP, Port)
- `src` - address source
- `buckets`, `attempts`, `bucket_type` - metadata
- `last_attempt`, `last_success`, `last_ban_time` - timestamps
