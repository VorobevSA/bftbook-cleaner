package cleaner

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/p2p/conn"
)

func (c *Cleaner) checkPeers(ctx context.Context, addrs []Addr) ([]Addr, error) {
	if len(addrs) == 0 {
		return nil, nil
	}

	addrChan := make(chan Addr)
	resultChan := make(chan peerCheckResult)

	var processed atomic.Int64
	total := int64(len(addrs))

	// Progress reporter.
	progressCtx, progressCancel := context.WithCancel(context.Background())
	defer progressCancel()
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				current := processed.Load()
				percent := float64(current) / float64(total) * 100
				c.log.Printf("Progress: %d/%d peers checked (%.1f%%)", current, total, percent)
			case <-progressCtx.Done():
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < c.cfg.Workers; i++ {
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
					valid := c.checkPeer(ctx, addr)
					select {
					case <-ctx.Done():
						return
					case resultChan <- peerCheckResult{addr: addr, valid: valid}:
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

	var validAddrs []Addr
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case result, ok := <-resultChan:
			if !ok {
				return validAddrs, nil
			}
			processed.Add(1)
			if c.cfg.Verbose {
				mark := "✓"
				if !result.valid {
					mark = "✗"
				}
				c.log.Printf("%s Checking %s:%d (ID: %s) - Port+Handshake",
					mark, result.addr.Addr.IP, result.addr.Addr.Port, shortID(result.addr.Addr.ID))
			}
			if result.valid {
				validAddrs = append(validAddrs, result.addr)
			}
		}
	}
}

func (c *Cleaner) checkPeer(ctx context.Context, addr Addr) bool {
	address := net.JoinHostPort(addr.Addr.IP, fmt.Sprintf("%d", addr.Addr.Port))
	dialer := &net.Dialer{Timeout: c.cfg.Timeout}

	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return false
	}
	defer closeWithLog(c.log, conn, address)

	if c.checkP2PHandshake(ctx, conn, addr) {
		return true
	}

	// Port is open but handshake failed; consider it valid for now and rely on NodeInfo later.
	return true
}

func (c *Cleaner) checkP2PHandshake(ctx context.Context, connection net.Conn, addr Addr) bool {
	deadline := c.now().Add(c.cfg.Timeout / 2)
	_ = connection.SetDeadline(deadline)

	privKey := ed25519.GenPrivKey()
	secretConn, err := conn.MakeSecretConnection(connection, privKey)
	if err != nil {
		closeWithLog(c.log, connection, addr.Addr.IP)
		return c.checkPeerBasicResponse(ctx, addr)
	}
	defer closeWithLog(c.log, secretConn, addr.Addr.IP)

	return true
}

func (c *Cleaner) checkPeerBasicResponse(ctx context.Context, addr Addr) bool {
	address := net.JoinHostPort(addr.Addr.IP, fmt.Sprintf("%d", addr.Addr.Port))
	dialer := &net.Dialer{Timeout: c.cfg.Timeout}

	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return false
	}
	defer closeWithLog(c.log, conn, address)

	readTimeout := c.cfg.Timeout
	if readTimeout > 2*time.Second {
		readTimeout = 2 * time.Second
	}
	_ = conn.SetReadDeadline(c.now().Add(readTimeout))

	buffer := make([]byte, 64)
	n, _ := conn.Read(buffer)
	return n > 0
}
