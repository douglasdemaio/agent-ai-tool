package live

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type AgentsResponse struct {
	Agents []Agent `json:"agents"`
}

type Agent struct {
	ID        string    `json:"id"`
	Card      AgentCard `json:"card"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type AgentCard struct {
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Version      string   `json:"version,omitempty"`
	URL          string   `json:"url,omitempty"`
	PublicKey    string   `json:"publicKey"`
	Capabilities []string `json:"capabilities,omitempty"`
	// Everything below is inside the card's canonical bytes, so a verifier that
	// does not model it cannot re-derive what was signed. A card parsed without
	// these fields still renders correctly and still reports every signature as
	// invalid, which is the worst kind of bug to ship: the page looks right and
	// the one thing it claims to check is false.
	Skills          []AgentSkill `json:"skills,omitempty"`
	Currencies      []string     `json:"currencies,omitempty"`
	SettlementModes []string     `json:"settlementModes,omitempty"`
	ProbeTarget     string       `json:"probeTarget,omitempty"`
}

type AgentSkill struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Tags   []string `json:"tags,omitempty"`
	Input  []string `json:"inputModes,omitempty"`
	Output []string `json:"outputModes,omitempty"`
}

// Health is the marketplace's own answer about itself.
//
// verificationKey is the only field that matters to a reader here, and it is the
// one the directory checks the rest of what vtessera says against: a signature is
// only as good as the key it names, and this is where the key is named.
type Health struct {
	Status          string `json:"status"`
	Version         string `json:"version,omitempty"`
	VerificationKey string `json:"verificationKey"`
	Cluster         string `json:"cluster,omitempty"`
	GenesisHash     string `json:"genesisHash,omitempty"`
	SettlementTier  string `json:"settlementTier,omitempty"`
	Sandbox         bool   `json:"sandbox,omitempty"`
}

func ValidateHealth(body []byte) error {
	var response Health
	if err := probe(body, "verificationKey", &response); err != nil {
		return err
	}
	if response.VerificationKey == "" {
		return errors.New("verificationKey is empty")
	}
	return nil
}

type MetricsResponse struct {
	GeneratedAt time.Time    `json:"generatedAt"`
	AsOf        *time.Time   `json:"asOf"`
	Totals      Totals       `json:"totals"`
	Agents      []AgentUsage `json:"agents"`
}

type Totals struct {
	Delivered int `json:"delivered"`
	Disputed  int `json:"disputed"`
	Cancelled int `json:"cancelled"`
	Consumers int `json:"consumers"`
	Services  int `json:"services"`
}

type AgentUsage struct {
	AgentID   string `json:"agentId"`
	Delivered int    `json:"delivered"`
	Disputed  int    `json:"disputed"`
	Cancelled int    `json:"cancelled"`
}

// StatusActive is the only status an agent is listed under. vtessera withholds
// retired and suspended agents from the default feed; this constant is here so a
// consumer of the feed and its renderer agree on which one that is.
const StatusActive = "active"

const BadgeFloor = 3

func ValidateAgents(body []byte) error {
	var response AgentsResponse
	if err := probe(body, "agents", &response); err != nil {
		return err
	}
	return nil
}

func ValidateMetrics(body []byte) error {
	var response MetricsResponse
	if err := probe(body, "totals", &response); err != nil {
		return err
	}
	if response.Totals.Delivered < 0 {
		return errors.New("totals.delivered is negative")
	}
	return nil
}

func probe(body []byte, requiredKey string, into any) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return fmt.Errorf("body is not a JSON object: %w", err)
	}
	if _, ok := fields[requiredKey]; !ok {
		return fmt.Errorf(`body has no %q key`, requiredKey)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return err
	}
	return nil
}

func (a AgentsResponse) Normalized() AgentsResponse {
	if a.Agents == nil {
		a.Agents = []Agent{}
	}
	return a
}

// UpdatedAt is the most recent update across the feed, used as the listing's
// last_verified so a live entry is never dated by hand.
func (a AgentsResponse) UpdatedAt() time.Time {
	var latest time.Time
	for _, agent := range a.Agents {
		if agent.UpdatedAt.After(latest) {
			latest = agent.UpdatedAt
		}
	}
	return latest
}

func (m MetricsResponse) Normalized() MetricsResponse {
	if m.Agents == nil {
		m.Agents = []AgentUsage{}
	}
	return m
}
