package content

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const validEntry = `{
  "slug": "vtessera",
  "name": "vtessera",
  "summary": "A2A agent marketplace.",
  "url": "https://example.com",
  "agent_card_url": null,
  "mcp_endpoint_url": null,
  "category": "marketplace",
  "access": "open source",
  "source": "curated",
  "last_verified": "2026-09-27T00:00:00Z"
}`

func TestLoadAcceptsAValidEntry(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "vtessera.json", validEntry)

	entries, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Slug != "vtessera" {
		t.Errorf("slug = %q", entries[0].Slug)
	}
	if entries[0].LastVerified.IsZero() {
		t.Error("last_verified did not parse")
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "bad.json", strings.Replace(validEntry, `"slug": "vtessera"`, `"slug": "vtessera", "price": "free"`, 1))

	_, err := Load(dir)
	if err == nil {
		t.Fatal("an unknown field should fail the build, not be silently ignored")
	}
	if !strings.Contains(err.Error(), "price") {
		t.Errorf("error should name the offending field, got: %v", err)
	}
}

func TestLoadRejectsDuplicateSlugs(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.json", validEntry)
	write(t, dir, "b.json", validEntry)

	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "duplicate slug") {
		t.Fatalf("want a duplicate-slug error, got: %v", err)
	}
}

func TestLoadRejectsMissingDirectory(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Errorf("an empty content set is a valid state, got: %v", err)
	}
}

func TestValidateRejectsUnusableFields(t *testing.T) {
	verified := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		mutate func(*Entry)
		want   string
	}{
		"relative url":         {func(e *Entry) { e.URL = "/relative" }, "absolute"},
		"javascript url":       {func(e *Entry) { e.URL = "javascript:alert(1)" }, "http or https"},
		"ftp url":              {func(e *Entry) { e.URL = "ftp://example.com" }, "http or https"},
		"empty url":            {func(e *Entry) { e.URL = "" }, "must not be empty"},
		"bad agent card url":   {func(e *Entry) { bad := "not a url"; e.AgentCardURL = &bad }, "absolute"},
		"bad mcp endpoint url": {func(e *Entry) { bad := "not a url"; e.MCPEndpointURL = &bad }, "mcp_endpoint_url"},
		"bad api url":          {func(e *Entry) { bad := "not a url"; e.APIURL = &bad }, "api_url"},
		"unknown source":       {func(e *Entry) { e.Source = "scraped" }, "source"},
		"uppercase slug":       {func(e *Entry) { e.Slug = "VteSSera" }, "slug"},
		"slug with underscore": {func(e *Entry) { e.Slug = "vtes_sera" }, "slug"},
		"empty name":           {func(e *Entry) { e.Name = "  " }, "name"},
		"empty summary":        {func(e *Entry) { e.Summary = "" }, "summary"},
		"missing verified":     {func(e *Entry) { e.LastVerified = time.Time{} }, "last_verified"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			entry := Entry{
				Slug: "vtessera", Name: "vtessera", Summary: "s",
				URL: "https://example.com", Source: SourceCurated, LastVerified: verified,
			}
			tc.mutate(&entry)
			err := entry.Validate()
			if err == nil {
				t.Fatalf("%s should be rejected", name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestLoadRejectsTrailingContent(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.json", validEntry+" {}")

	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "trailing content") {
		t.Fatalf("want a trailing-content error, got: %v", err)
	}
}

func TestValidateAcceptsOptionalFieldsOmitted(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.json", `{
	  "slug": "bare",
	  "name": "Bare",
	  "summary": "Only the required fields.",
	  "url": "https://example.com",
	  "source": "curated",
	  "last_verified": "2026-09-27T00:00:00Z"
	}`)

	entries, err := Load(dir)
	if err != nil {
		t.Fatalf("optional fields should be omittable, got: %v", err)
	}
	if entries[0].AgentCardURL != nil || entries[0].MCPEndpointURL != nil {
		t.Error("omitted optional URLs should stay nil, not become empty strings")
	}
}

func TestReviewDue(t *testing.T) {
	now := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		entry  Entry
		expect bool
	}{
		{
			name:   "confirmed yesterday",
			entry:  Entry{Source: SourceCurated, LastVerified: now.AddDate(0, 0, -1)},
			expect: false,
		},
		{
			name:   "confirmed just inside the window",
			entry:  Entry{Source: SourceCurated, LastVerified: now.Add(-ReviewWindow + 24*time.Hour)},
			expect: false,
		},
		{
			name:   "confirmed just outside the window",
			entry:  Entry{Source: SourceCurated, LastVerified: now.Add(-ReviewWindow - 24*time.Hour)},
			expect: true,
		},
		{
			name:   "a live entry is never due",
			entry:  Entry{Source: SourceLive, LastVerified: now.AddDate(-2, 0, 0)},
			expect: false,
		},
		{
			// A future date is a data error. Treating it as overdue would flag a
			// working entry, and flags that fire on correct data get ignored.
			name:   "a future date is not due",
			entry:  Entry{Source: SourceCurated, LastVerified: now.AddDate(0, 6, 0)},
			expect: false,
		},
	}

	for _, tc := range cases {
		if got := tc.entry.ReviewDue(now); got != tc.expect {
			t.Errorf("%s: ReviewDue = %v, want %v", tc.name, got, tc.expect)
		}
	}
}

// The auth field is stated in words, so "none" has to be read as a statement
// rather than as a value. Getting this backwards is what made the pages claim
// a token was required for a call the entry itself declared open.
func TestACallThatDeclaresNoAuthNeedsNoToken(t *testing.T) {
	for _, tc := range []struct {
		auth string
		want bool
	}{
		{"none", false},
		{"None", false},
		{"", false},
		{"bearer", true},
		{"signature", true},
	} {
		call := Call{Auth: tc.auth}
		if got := call.RequiresToken(); got != tc.want {
			t.Errorf("RequiresToken(auth=%q) = %v, want %v", tc.auth, got, tc.want)
		}
	}
}
