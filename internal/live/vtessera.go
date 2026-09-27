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
