package lab

// Curated server-owned dictionaries keep anonymous handles safe and predictable.
// Each style offers 256 base names; the hub adds suffixes for active collisions.
var nameStyles = map[string]struct{ prefixes, nouns []string }{
	"systems": {
		[]string{"atomic", "async", "concurrent", "parallel", "lazy", "immutable", "buffered", "recursive", "lockfree", "cached", "compiled", "native", "virtual", "typed", "binary", "volatile"},
		[]string{"goroutine", "mutex", "channel", "kernel", "pointer", "thread", "semaphore", "compiler", "runtime", "allocator", "iterator", "closure", "lambda", "opcode", "register", "daemon"},
	},
	"network": {
		[]string{"streaming", "routed", "encrypted", "reliable", "resilient", "connected", "multiplexed", "distributed", "replicated", "federated", "loopback", "duplex", "broadcast", "unicast", "persistent", "stateless"},
		[]string{"packet", "socket", "gateway", "proxy", "router", "handshake", "datagram", "tunnel", "beacon", "endpoint", "bridge", "subnet", "resolver", "stream", "heartbeat", "replica"},
	},
	"terminal": {
		[]string{"sudo", "root", "shell", "bash", "zsh", "vim", "git", "grep", "awk", "sed", "tmux", "curl", "ssh", "chmod", "make", "debug"},
		[]string{"ninja", "wizard", "hacker", "artisan", "pilot", "ranger", "nomad", "sage", "tinkerer", "explorer", "alchemist", "keeper", "surfer", "rider", "smith", "captain"},
	},
	"pioneers": {
		[]string{"brave", "sleepy", "focused", "clever", "graceful", "curious", "patient", "bright", "inspired", "fearless", "inventive", "thoughtful", "playful", "witty", "spirited", "relentless"},
		[]string{"hopper", "turing", "lovelace", "ritchie", "hamilton", "lamport", "dijkstra", "knuth", "thompson", "liskov", "mccarthy", "backus", "shannon", "engelbart", "bernerslee", "goldberg"},
	},
}

func validNameStyle(style string) bool {
	_, ok := nameStyles[style]
	return style == "" || ok
}
