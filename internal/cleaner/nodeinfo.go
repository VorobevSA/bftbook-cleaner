package cleaner

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	cmtbytes "github.com/cometbft/cometbft/libs/bytes"
	"github.com/cometbft/cometbft/libs/protoio"
	tmp2p "github.com/cometbft/cometbft/proto/tendermint/p2p"
	"github.com/cometbft/cometbft/version"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/p2p/conn"
)

type peerNodeInfo = p2p.DefaultNodeInfo

func (c *Cleaner) enrichWithNodeInfo(ctx context.Context, addrs []Addr) ([]Addr, error) {
	if len(addrs) == 0 {
		return nil, nil
	}

	workerCount := c.cfg.Workers
	if workerCount <= 0 {
		workerCount = 1
	}

	c.log.Printf("Fetching NodeInfo for %d peers using %d workers...", len(addrs), workerCount)

	addrChan := make(chan Addr)
	resultChan := make(chan nodeInfoResult)

	var checked atomic.Int64
	total := int64(len(addrs))

	// Progress reporter
	progressCancel := startProgressReporter(
		&checked,
		total,
		"Progress: %d/%d NodeInfo fetched (%.1f%%)",
		c.log,
	)
	defer progressCancel()

	// Start workers
	wg := startWorkers(ctx, workerCount, addrChan, resultChan, func(ctx context.Context, addr Addr) nodeInfoResult {
		info, err := c.fetchNodeInfo(ctx, addr)
		return nodeInfoResult{addr: addr, info: info, err: err}
	})

	// Feed items to workers
	feedItems(ctx, addrs, addrChan)

	// Close result channel when all workers are done
	go func() {
		wg.Wait()
		close(resultChan)
		progressCancel()
	}()

	filtered := make([]Addr, 0, len(addrs))
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case result, ok := <-resultChan:
			if !ok {
				return filtered, nil
			}
			checked.Add(1)

			if result.err != nil {
				c.log.Printf("%s@%s:%d error: %v",
					result.addr.Addr.ID, result.addr.Addr.IP, result.addr.Addr.Port, result.err)
				continue
			}

			info := result.info
			if c.cfg.Verbose {
				c.log.Printf("NodeInfo: %s:%d (ID: %s) moniker=%s network=%s version=%s proto[p2p=%d block=%d app=%d] listen=%s tx_index=%s rpc=%s",
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

			if c.cfg.FilterNetwork != "" && info.Network != c.cfg.FilterNetwork {
				if c.cfg.Verbose {
					c.log.Printf("NodeInfo filter: dropping %s (network %s != %s)",
						shortID(result.addr.Addr.ID), info.Network, c.cfg.FilterNetwork)
				}
				continue
			}
			if c.cfg.FilterVersion != "" && info.Version != c.cfg.FilterVersion {
				if c.cfg.Verbose {
					c.log.Printf("NodeInfo filter: dropping %s (version %s != %s)",
						shortID(result.addr.Addr.ID), info.Version, c.cfg.FilterVersion)
				}
				continue
			}

			filtered = append(filtered, result.addr)
		}
	}
}

func (c *Cleaner) fetchNodeInfo(ctx context.Context, addr Addr) (*peerNodeInfo, error) {
	address := net.JoinHostPort(addr.Addr.IP, fmt.Sprintf("%d", addr.Addr.Port))
	dialer := &net.Dialer{Timeout: c.cfg.Timeout}

	tcpConn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("dial failed: %w", err)
	}
	defer closeWithLog(c.log, tcpConn, address, c.cfg.Verbose)

	if err := tcpConn.SetDeadline(time.Now().Add(c.cfg.Timeout)); err != nil {
		return nil, fmt.Errorf("set deadline: %w", err)
	}

	privKey := ed25519.GenPrivKey()
	secretConn, err := conn.MakeSecretConnection(tcpConn, privKey)
	if err != nil {
		return nil, fmt.Errorf("secret connection failed: %w", err)
	}
	defer closeWithLog(c.log, secretConn, address, c.cfg.Verbose)

	if err := secretConn.SetDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("reset deadline: %w", err)
	}

	localInfo := buildLocalNodeInfo(privKey)
	peerInfo, err := exchangeNodeInfo(ctx, secretConn, c.cfg.Timeout, localInfo)
	if err != nil {
		return nil, err
	}
	return &peerInfo, nil
}

func exchangeNodeInfo(ctx context.Context, cconn net.Conn, timeout time.Duration, ourInfo p2p.DefaultNodeInfo) (p2p.DefaultNodeInfo, error) {
	if err := cconn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return p2p.DefaultNodeInfo{}, err
	}

	// Use a struct to track which operation failed
	type opResult struct {
		op  string // "write" or "read"
		err error
	}

	resultChan := make(chan opResult, nodeInfoExchangeOps)
	var remote tmp2p.DefaultNodeInfo

	// Write our NodeInfo to the peer
	go func() {
		_, err := protoio.NewDelimitedWriter(cconn).WriteMsg(ourInfo.ToProto())
		resultChan <- opResult{op: "write", err: err}
	}()

	// Read peer's NodeInfo
	go func() {
		reader := protoio.NewDelimitedReader(cconn, p2p.MaxNodeInfoSize())
		_, err := reader.ReadMsg(&remote)
		resultChan <- opResult{op: "read", err: err}
	}()

	var errors []error
	completed := 0
	timeoutChan := time.After(timeout)

	for completed < nodeInfoExchangeOps {
		select {
		case result := <-resultChan:
			completed++
			if result.err != nil {
				errors = append(errors, fmt.Errorf("%s NodeInfo failed: %w", result.op, result.err))
			}
		case <-ctx.Done():
			return p2p.DefaultNodeInfo{}, fmt.Errorf("context canceled during NodeInfo exchange: %w", ctx.Err())
		case <-timeoutChan:
			return p2p.DefaultNodeInfo{}, fmt.Errorf("timeout waiting for NodeInfo exchange (completed %d/%d operations)", completed, nodeInfoExchangeOps)
		}
	}

	// If any operation failed, return a combined error
	if len(errors) > 0 {
		errMsg := "NodeInfo exchange failed:"
		for _, err := range errors {
			errMsg += " " + err.Error() + ";"
		}
		return p2p.DefaultNodeInfo{}, fmt.Errorf("%s", errMsg)
	}

	if err := cconn.SetDeadline(time.Time{}); err != nil {
		return p2p.DefaultNodeInfo{}, fmt.Errorf("failed to reset deadline: %w", err)
	}

	info, err := p2p.DefaultNodeInfoFromToProto(&remote)
	if err != nil {
		return p2p.DefaultNodeInfo{}, fmt.Errorf("failed to parse NodeInfo: %w", err)
	}

	return info, nil
}

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

// GetNodeIDByIPPort fetches node ID from a CometBFT node by its IP address and port.
// It establishes a P2P connection, performs handshake, and returns the node ID.
func (c *Cleaner) GetNodeIDByIPPort(ctx context.Context, ip string, port int) (string, error) {
	addr := Addr{
		Addr: Address{
			ID:   "", // ID is unknown, will be fetched
			IP:   ip,
			Port: port,
		},
	}

	info, err := c.fetchNodeInfo(ctx, addr)
	if err != nil {
		return "", fmt.Errorf("failed to fetch node info from %s:%d: %w", ip, port, err)
	}

	return string(info.DefaultNodeID), nil
}

func shortID(id string) string {
	if len(id) <= shortIDLength {
		return id
	}
	return id[:shortIDLength]
}
