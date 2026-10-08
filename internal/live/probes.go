package live

// ProbeAgents are the test agents this repository registers against vtessera.
//
// They exist so the marketplace can be exercised end to end — offers, trades,
// receipts, caps — without waiting for a stranger to show up, which means every
// trade they have recorded is our own test traffic. Publishing those figures
// under a banner that reads "what agents are actually doing" would present our
// tests as outside usage, and the distinction is the whole reason a usage
// number is worth showing at all.
//
// The list is curated rather than inferred from a name: an agent's name is
// chosen by whoever registered it, so a prefix like "probe-" is evidence about
// naming rather than about who is trading. An ID here is a statement about who
// holds that key.
//
// Nothing here deletes or rewrites ledger data. These IDs only decide what this
// directory is willing to claim about it.
var ProbeAgents = []string{
	"Ax1YCpc9L35TRAHgGN5CUjPe3BxT6bVsjq2xci5hPCgV",
	"5kgpQFCrpcVgdcHqjE2z9GejeCpPZQTohfoeas6VRu9Q",
	"HR8yzDpPdSYKmAo6GG2XbSrzvWGnxBDkLkowohHbdUYq",
	"7YFTHXFy2Aon9YxBEhbnk59nmcvdEiGX3xiPBJGcnToA",
	"545bUJCXUwW2CZrSmtvqjfF164yvb8F1vwqKQ8M6Qrdo",
	"5bPBmVAC9Ywbn6B4aAC3gEduwFq1zHtRXRFPrmh2ZVQw",
}

// IsProbeAgent reports whether an agent ID belongs to this repository's own
// test agents.
func IsProbeAgent(id string) bool {
	for _, probe := range ProbeAgents {
		if probe == id {
			return true
		}
	}
	return false
}

// AllProbes reports whether every agent that produced a row in this response is
// one of ours. It is false when there are no rows at all: an empty response
// says nothing about who is trading, and claiming test-only activity from no
// evidence would be its own kind of invention.
func (m MetricsResponse) AllProbes() bool {
	if len(m.Agents) == 0 {
		return false
	}
	for _, agent := range m.Agents {
		if !IsProbeAgent(agent.AgentID) {
			return false
		}
	}
	return true
}

// AnyProbes reports whether any row was produced by one of ours. Totals that
// mix our traffic with someone else's are still our traffic, so a reader has to
// be told the number includes it.
func (m MetricsResponse) AnyProbes() bool {
	for _, agent := range m.Agents {
		if IsProbeAgent(agent.AgentID) {
			return true
		}
	}
	return false
}
