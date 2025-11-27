package cleaner

// AddrBook represents the structure of CometBFT addrbook.json.
type AddrBook struct {
	Key   string `json:"key"`
	Addrs []Addr `json:"addrs"`
}

// Addr represents a single address entry in the addrbook.
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

// Address represents IP and port information for peers.
type Address struct {
	ID   string `json:"id"`
	IP   string `json:"ip"`
	Port int    `json:"port"`
}

type peerCheckResult struct {
	addr  Addr
	valid bool
}

type nodeInfoResult struct {
	addr Addr
	info *peerNodeInfo
	err  error
}
