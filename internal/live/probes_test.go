package live

import "testing"

func TestProbeAgentsAreIdentifiedByIDNotByTheNameTheyChose(t *testing.T) {
	if !IsProbeAgent("Ax1YCpc9L35TRAHgGN5CUjPe3BxT6bVsjq2xci5hPCgV") {
		t.Error("a registered probe agent is not recognised as one")
	}
	if IsProbeAgent("not-a-probe") {
		t.Error("an unknown agent was claimed as ours")
	}
	if len(ProbeAgents) != 6 {
		t.Errorf("ProbeAgents has %d entries, want the six registered test agents", len(ProbeAgents))
	}
}

func TestAnEmptyMetricsResponseClaimsNothingAboutWhoIsTrading(t *testing.T) {
	empty := MetricsResponse{}
	if empty.AllProbes() {
		t.Error("a response with no agent rows cannot say every agent is ours")
	}
	if empty.AnyProbes() {
		t.Error("a response with no agent rows cannot say one of ours traded")
	}
}

func TestAProbeInTheMixMarksTheWholeResponseAsIncludingTests(t *testing.T) {
	mixed := MetricsResponse{Agents: []AgentUsage{
		{AgentID: "outside-agent", Delivered: 4},
		{AgentID: ProbeAgents[0], Delivered: 2},
	}}
	if mixed.AllProbes() {
		t.Error("a response containing an outside agent is not test-only")
	}
	if !mixed.AnyProbes() {
		t.Error("a response containing one of ours must say so, since the totals include it")
	}
}
