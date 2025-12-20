package cleaner

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"

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

	// Progress reporter
	progressCancel := startProgressReporter(
		&processed,
		total,
		"Progress: %d/%d peers checked (%.1f%%)",
		c.log,
	)
	defer progressCancel()

	// Start workers
	wg := startWorkers(ctx, c.cfg.Workers, addrChan, resultChan, func(ctx context.Context, addr Addr) peerCheckResult {
		valid := c.checkPeer(ctx, addr)
		return peerCheckResult{addr: addr, valid: valid}
	})

	// Feed items to workers
	feedItems(ctx, addrs, addrChan)

	// Close result channel when all workers are done
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
	defer closeWithLog(c.log, conn, address, c.cfg.Verbose)

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
		closeWithLog(c.log, connection, addr.Addr.IP, c.cfg.Verbose)
		return c.checkPeerBasicResponse(ctx, addr)
	}
	defer closeWithLog(c.log, secretConn, addr.Addr.IP, c.cfg.Verbose)

	return true
}

func (c *Cleaner) checkPeerBasicResponse(ctx context.Context, addr Addr) bool {
	address := net.JoinHostPort(addr.Addr.IP, fmt.Sprintf("%d", addr.Addr.Port))
	dialer := &net.Dialer{Timeout: c.cfg.Timeout}

	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return false
	}
	defer closeWithLog(c.log, conn, address, c.cfg.Verbose)

	readTimeout := c.cfg.Timeout
	if readTimeout > maxBasicReadTimeout {
		readTimeout = maxBasicReadTimeout
	}
	_ = conn.SetReadDeadline(c.now().Add(readTimeout))

	buffer := make([]byte, peerResponseBufferSize)
	n, _ := conn.Read(buffer)
	return n > 0
}
