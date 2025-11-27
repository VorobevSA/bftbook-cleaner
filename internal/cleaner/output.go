package cleaner

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

func (c *Cleaner) updatePeerTimestamps(addrs []Addr) {
	now := c.now().UTC().Format(time.RFC3339)
	for i := range addrs {
		addrs[i].Attempts = 0
		addrs[i].LastAttempt = now
		addrs[i].LastSuccess = now
		addrs[i].LastBanTime = defaultBanTimestamp
	}
}

func (c *Cleaner) generateKey() string {
	return fmt.Sprintf("%x", c.now().UnixNano())
}

func (c *Cleaner) writeAddrBook(book AddrBook) error {
	file, err := os.Create(c.cfg.OutputFile)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "\t")
	return encoder.Encode(book)
}
