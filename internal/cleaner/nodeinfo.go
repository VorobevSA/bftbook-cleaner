package cleaner

import (
	"context"
	"fmt"
	"net"
	"sync"
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

	progressCtx, progressCancel := context.WithCancel(context.Background())
	defer progressCancel()
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				current := checked.Load()
				percent := float64(current) / float64(total) * 100
				c.log.Printf("Progress: %d/%d NodeInfo fetched (%.1f%%)", current, total, percent)
			case <-progressCtx.Done():
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case addr, ok := <-addrChan:
					if !ok {
						return
					}
					info, err := c.fetchNodeInfo(ctx, addr)
					select {
					case <-ctx.Done():
						return
					case resultChan <- nodeInfoResult{addr: addr, info: info, err: err}:
					}
				}
			}
		}()
	}

	go func() {
		defer close(addrChan)
		for _, addr := range addrs {
			select {
			case <-ctx.Done():
				return
			case addrChan <- addr:
			}
		}
	}()

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
	defer tcpConn.Close()

	if err := tcpConn.SetDeadline(c.now().Add(c.cfg.Timeout)); err != nil {
		return nil, fmt.Errorf("set deadline: %w", err)
	}

	privKey := ed25519.GenPrivKey()
	secretConn, err := conn.MakeSecretConnection(tcpConn, privKey)
	if err != nil {
		return nil, fmt.Errorf("secret connection failed: %w", err)
	}
	defer secretConn.Close()

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

	errc := make(chan error, 2)
	var remote tmp2p.DefaultNodeInfo

	go func() {
		_, err := protoio.NewDelimitedWriter(cconn).WriteMsg(ourInfo.ToProto())
		errc <- err
	}()

	go func() {
		reader := protoio.NewDelimitedReader(cconn, p2p.MaxNodeInfoSize())
		_, err := reader.ReadMsg(&remote)
		errc <- err
	}()

	completed := 0
	for completed < 2 {
		select {
		case err := <-errc:
			if err != nil {
				return p2p.DefaultNodeInfo{}, err
			}
			completed++
		case <-ctx.Done():
			return p2p.DefaultNodeInfo{}, ctx.Err()
		case <-time.After(timeout):
			return p2p.DefaultNodeInfo{}, fmt.Errorf("timeout waiting for NodeInfo exchange")
		}
	}

	if err := cconn.SetDeadline(time.Time{}); err != nil {
		return p2p.DefaultNodeInfo{}, err
	}

	info, err := p2p.DefaultNodeInfoFromToProto(&remote)
	if err != nil {
		return p2p.DefaultNodeInfo{}, err
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

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
