package live

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// AgentReport is everything fetched about one agent, kept per agent so a failure
// on one does not cost the directory the rest.
//
// Error is separate from a missing probe on purpose. "This request failed" and
// "nobody has ever probed this agent" are different facts, and collapsing them
// would let a broken fetch render as a clean bill of health for a capability list
// that was never checked.
type AgentReport struct {
	AgentID     string          `json:"agentId"`
	Attestation CardAttestation `json:"attestation"`
	Probe       *ProbeReport    `json:"probe,omitempty"`
	Error       string          `json:"error,omitempty"`
}

// AgentReportsResponse is the whole per-agent feed, keyed by nothing: the order is
// the agents feed's order, so a reader diffing two snapshots sees the same rows in
// the same places.
type AgentReportsResponse struct {
	Agents []AgentReport `json:"agents"`
}

func ValidateAgentReports(body []byte) error {
	var response AgentReportsResponse
	if err := probe(body, "agents", &response); err != nil {
		return err
	}
	for _, report := range response.Agents {
		if report.AgentID == "" {
			return errors.New("an agent report has no agentId")
		}
	}
	return nil
}

func (a AgentReportsResponse) Normalized() AgentReportsResponse {
	if a.Agents == nil {
		a.Agents = []AgentReport{}
	}
	return a
}

func (a AgentReportsResponse) ByAgent() map[string]AgentReport {
	out := make(map[string]AgentReport, len(a.Agents))
	for _, report := range a.Agents {
		out[report.AgentID] = report
	}
	return out
}

// notProbed is the code a marketplace returns for an agent that exists but has
// never been probed. It is matched rather than any 404, because a 404 from a
// different cause means the endpoint moved, and treating that as "nobody has
// checked this" would be a clean bill of health for a broken path.
const notProbed = "NOT_PROBED"

// LoadAgentReports fetches one attestation and one probe report per agent.
//
// This does not go through Load because that handles a single path, and this feed
// is one request per agent. The cache still wins over the network on any failure,
// exactly as it does for a single path, so a build that cannot reach the
// marketplace renders the committed snapshot and says it is a snapshot.
func LoadAgentReports(ctx context.Context, baseURL string, agents []Agent, cachePath string, client *http.Client) Result {
	if baseURL != "" && client != nil && len(agents) > 0 {
		feed, err := fetchAgentReports(ctx, baseURL, agents, client)
		if err == nil {
			encoded, marshalErr := json.Marshal(feed)
			if marshalErr != nil {
				return Result{FetchErr: marshalErr}
			}
			return Result{Response: encoded}
		}
		snapshot, cacheErr := readCache(cachePath)
		if cacheErr == nil {
			return Result{
				Response:  snapshot.Response,
				FromCache: true,
				FetchedAt: snapshot.FetchedAt,
				FetchErr:  err,
			}
		}
		if errors.Is(cacheErr, ErrNoCache) {
			return Result{FetchErr: err}
		}
		return Result{FetchErr: errors.Join(err, cacheErr)}
	}
	snapshot, err := readCache(cachePath)
	if err != nil {
		return Result{FetchErr: err}
	}
	return Result{Response: snapshot.Response, FromCache: true, FetchedAt: snapshot.FetchedAt}
}

func RefreshAgentReports(ctx context.Context, baseURL string, agents []Agent, cachePath string, client *http.Client) error {
	if baseURL == "" {
		return fmt.Errorf("no base URL configured, refusing to write a snapshot")
	}
	if len(agents) == 0 {
		return fmt.Errorf("no agents to report on, refusing to write an empty snapshot")
	}
	feed, err := fetchAgentReports(ctx, baseURL, agents, client)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(feed)
	if err != nil {
		return err
	}
	if err := ValidateAgentReports(encoded); err != nil {
		return fmt.Errorf("refusing to cache a malformed agent report feed: %w", err)
	}
	return WriteSnapshot(cachePath, encoded, time.Now().UTC())
}

// fetchAgentReports returns nil, nil when no agent answered, which is a real
// state rather than a fault: an empty marketplace has nothing to attest.
func fetchAgentReports(ctx context.Context, baseURL string, agents []Agent, client *http.Client) (*AgentReportsResponse, error) {
	feed := &AgentReportsResponse{Agents: []AgentReport{}}
	answered := 0
	for _, agent := range agents {
		report := AgentReport{AgentID: agent.ID}
		attestation, err := fetchJSON(ctx, client, baseURL+"/v1/agents/"+agent.ID+"/attestation", &report.Attestation)
		if err != nil {
			report.Error = err.Error()
		} else if attestation {
			answered++
		}
		probe, probed, err := fetchProbe(ctx, client, baseURL+"/v1/agents/"+agent.ID+"/capabilities")
		switch {
		case err != nil && report.Error == "":
			report.Error = err.Error()
		case err != nil:
			report.Error = report.Error + "; " + err.Error()
		case probed:
			report.Probe = probe
		}
		feed.Agents = append(feed.Agents, report)
	}
	if answered == 0 {
		return nil, fmt.Errorf("no agent answered an attestation request")
	}
	return feed, nil
}

// fetchProbe reports probed=false for a marketplace that says the agent has never
// been probed, which is an answer rather than a failure.
func fetchProbe(ctx context.Context, client *http.Client, url string) (*ProbeReport, bool, error) {
	var report ProbeReport
	ok, err := fetchJSON(ctx, client, url, &report)
	if err != nil {
		if isNotProbed(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &report, ok, nil
}

func isNotProbed(err error) bool {
	return err != nil && strings.Contains(err.Error(), notProbed)
}

// fetchJSON decodes one response, reporting ok=false when the endpoint answered
// with a refusal that carries a code. A 404 here is expected rather than
// exceptional, so it is not allowed to abort a feed.
func fetchJSON(ctx context.Context, client *http.Client, url string, into any) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return false, err
	}
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(body, &failure) == nil && failure.Code != "" {
			return false, fmt.Errorf("GET %s = %d %s", path(url), resp.StatusCode, failure.Code)
		}
		return false, fmt.Errorf("GET %s = %d", path(url), resp.StatusCode)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return false, fmt.Errorf("GET %s is not the expected shape: %w", path(url), err)
	}
	return true, nil
}

// path strips the base URL off an error message, so a feed with one failing agent
// does not print the same host on every line.
func path(raw string) string {
	if i := strings.Index(raw, "/v1/"); i > 0 {
		return raw[i:]
	}
	if i := strings.Index(raw, "/healthz"); i > 0 {
		return raw[i:]
	}
	return raw
}
