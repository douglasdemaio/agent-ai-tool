package drafts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

// live is opt-in because it is the only test in this repository that opens a
// socket to somebody else's server. The hermetic suite must not depend on twenty
// unrelated hosts staying up, so this is `make drafts-verify` and nothing else.
var live = flag.Bool("live", false, "re-fetch every draft's card and service URL")

func TestEveryDraftStillVerifiesOnTheWire(t *testing.T) {
	if !*live {
		t.Skip("pass -live, or run make drafts-verify")
	}
	client := &http.Client{Timeout: 25 * time.Second}
	for _, d := range load(t) {
		t.Run(d.Slug, func(t *testing.T) {
			if err := checkCard(client, d); err != nil {
				t.Error(err)
			}
			if err := checkService(client, d); err != nil {
				t.Error(err)
			}
		})
	}
}

func fetch(client *http.Client, target string) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json, text/html;q=0.8")
	req.Header.Set("User-Agent", "agent-ai-tool-directory/1.0 (+https://github.com/douglasdemaio/agent-ai-tool)")
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

// checkCard re-reads the card and compares its hash, so a service that rewrote
// its own listing fails the check instead of quietly riding on a draft that was
// written against different text.
func checkCard(client *http.Client, d Draft) error {
	status, body, err := fetch(client, d.Evidence.CardURL)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("the card now answers %d; it answered 200 when the draft was written", status)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return fmt.Errorf("the card is no longer JSON: %v", err)
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != d.Evidence.CardSHA256 {
		return fmt.Errorf("the card changed since the draft was written (was %s, now %s), so the draft needs reviewing",
			short(d.Evidence.CardSHA256), short(got))
	}
	return nil
}

func checkService(client *http.Client, d Draft) error {
	status, _, err := fetch(client, d.Evidence.ServiceURL)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("the service answers %d; it answered 200 when the draft was written", status)
	}
	return nil
}

func short(sum string) string {
	if len(sum) <= 12 {
		return sum
	}
	return sum[:6] + "…" + sum[len(sum)-4:]
}
