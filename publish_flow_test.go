package main

// Guards the published shape of how_to_call: that agents.json stays parseable in
// the documented shape, that a call's path is relative, and that no live entry
// regresses to a zero date.
//
// The live-service round trip is deliberately not in this file. It would need a
// base58 dependency that the site has no other reason to carry, and a test that
// needs the network stops being a unit test. The flow is exercised against the
// live service by hand; see docs in the vtessera repo.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type pubAuth struct {
	Type              string           `json:"type"`
	Credential        string           `json:"credential"`
	KeyEncoding       string           `json:"key_encoding"`
	SignatureEncoding string           `json:"signature_encoding"`
	SignedMessage     string           `json:"signed_message"`
	Steps             []map[string]any `json:"steps"`
}

type pubCall struct {
	Name        string         `json:"name"`
	Method      string         `json:"method"`
	Path        string         `json:"path"`
	ContentType string         `json:"content_type"`
	Auth        string         `json:"auth"`
	Body        map[string]any `json:"body"`
	Returns     string         `json:"returns"`
}

type pubHow struct {
	Note  string    `json:"note"`
	Auth  *pubAuth  `json:"auth"`
	Calls []pubCall `json:"calls"`
}

type pubAgent struct {
	Slug         string  `json:"slug"`
	URL          string  `json:"url"`
	Source       string  `json:"source"`
	LastVerified string  `json:"last_verified"`
	MCPEndpoint  *string `json:"mcp_endpoint_url"`
	How          *pubHow `json:"how_to_call"`
}

type pubDoc struct {
	Agents []pubAgent `json:"agents"`
}

func readAgentsJSON(t *testing.T) pubDoc {
	t.Helper()
	raw, err := os.ReadFile("public/agents.json")
	if err != nil {
		t.Skip("agents.json not built")
	}
	var doc pubDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestPublishedHowToCallIsMachineReadable(t *testing.T) {
	doc := readAgentsJSON(t)
	var vt *pubAgent
	for i := range doc.Agents {
		if doc.Agents[i].Slug == "vtessera" {
			vt = &doc.Agents[i]
		}
	}
	if vt == nil {
		t.Fatal("vtessera missing from agents.json")
	}
	if vt.How == nil {
		t.Fatal("vtessera publishes no how_to_call")
	}
	if vt.How.Auth == nil {
		t.Fatal("no auth block")
	}
	if vt.How.Auth.KeyEncoding != "base58" || vt.How.Auth.SignatureEncoding != "base64" {
		t.Errorf("encodings wrong: key=%q sig=%q", vt.How.Auth.KeyEncoding, vt.How.Auth.SignatureEncoding)
	}
	if !strings.Contains(vt.How.Auth.SignedMessage, "vtessera/auth/v1") {
		t.Error("signed_message omits the domain string, so a caller cannot reconstruct it")
	}
	if len(vt.How.Auth.Steps) != 2 {
		t.Errorf("expected 2 auth steps, got %d", len(vt.How.Auth.Steps))
	}
	if len(vt.How.Calls) == 0 {
		t.Fatal("no calls published")
	}
	sawRoute := false
	for _, c := range vt.How.Calls {
		if c.Method == "" || c.Path == "" {
			t.Errorf("incomplete call: %+v", c)
		}
		if !strings.HasPrefix(c.Path, "/") {
			t.Errorf("call path must be relative, got %q", c.Path)
		}
		if c.Returns == "" {
			t.Errorf("call %q publishes no returns, so its failure modes are invisible", c.Name)
		}
		if c.Path == "/agp/route" {
			sawRoute = true
			if c.Body == nil {
				t.Error("agp/route publishes no body")
			}
		}
	}
	if !sawRoute {
		t.Error("agp/route is not published, so the one real call is undocumented")
	}
}

// The empty marketplace must not date the listing at year 1; that reaches
// agents.json, the JSON-LD, and the sitemap.
func TestPublishedEntriesAreNeverZeroDated(t *testing.T) {
	doc := readAgentsJSON(t)
	for _, a := range doc.Agents {
		if strings.HasPrefix(a.LastVerified, "0001-01-01") {
			t.Errorf("%s carries a zero date", a.Slug)
		}
	}
}
