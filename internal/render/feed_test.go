package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemaio/agent-ai-tool/internal/content"
)

// feedSite is a build that keeps a change feed. The path is fixed across
// renders, because remembering the last build is the whole job.
func feedSite(t *testing.T, entries ...content.Entry) Site {
	t.Helper()
	s := site(t, entries...)
	s.ChangesPath = filepath.Join(t.TempDir(), "changes.json")
	s.RecordChanges = true
	return s
}

func publishedFeed(t *testing.T, out string) publishedChangeFeed {
	t.Helper()
	var feed publishedChangeFeed
	if err := json.Unmarshal([]byte(read(t, out, "changes.json")), &feed); err != nil {
		t.Fatalf("decoding the published change feed: %v", err)
	}
	return feed
}

func committedFeed(t *testing.T, path string) changeFeedFile {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the committed change feed: %v", err)
	}
	var feed changeFeedFile
	if err := json.Unmarshal(body, &feed); err != nil {
		t.Fatalf("decoding the committed change feed: %v", err)
	}
	return feed
}

// The feed's contract: two consecutive builds with a real difference produce
// one correct record — what arrived, what left, and what changed where —
// while the first build's own state stays visible as the record it wrote.
func TestTheFeedNamesEntriesAddedRemovedAndChangedInOneBuild(t *testing.T) {
	alpha := entry("alpha", "First.")
	beta := entry("beta", "Second.")
	gamma := entry("gamma", "Third.")
	s := feedSite(t, alpha, beta, gamma)
	first := renderTo(t, s)

	bootstrap := publishedFeed(t, first)
	if len(bootstrap.Records) != 1 {
		t.Fatalf("first build published %d records, want 1", len(bootstrap.Records))
	}
	if got := strings.Join(bootstrap.Records[0].Added, ","); got != "alpha,beta,gamma" {
		t.Errorf("bootstrap added = %q, want every entry there was", got)
	}
	if bootstrap.Records[0].Removed != nil || bootstrap.Records[0].Changed != nil {
		t.Errorf("a first build records arrivals only, got removed=%v changed=%v",
			bootstrap.Records[0].Removed, bootstrap.Records[0].Changed)
	}

	rewritten := beta
	rewritten.Summary = "Second, rewritten."
	s.Entries = []content.Entry{alpha, entry("delta", "Fourth."), rewritten}
	second := renderTo(t, s)

	got := publishedFeed(t, second)
	if len(got.Records) != 2 {
		t.Fatalf("second build published %d records, want 2", len(got.Records))
	}
	rec := got.Records[0]
	if strings.Join(rec.Added, ",") != "delta" {
		t.Errorf("added = %v, want the entry that arrived", rec.Added)
	}
	if strings.Join(rec.Removed, ",") != "gamma" {
		t.Errorf("removed = %v, want the entry that left", rec.Removed)
	}
	if len(rec.Changed) != 1 || rec.Changed[0].Slug != "beta" {
		t.Fatalf("changed = %v, want the one entry that moved", rec.Changed)
	}
	if fields := strings.Join(rec.Changed[0].Fields, ","); fields != "summary" {
		t.Errorf("fields = %q, want the field agents.json now publishes differently", fields)
	}

	// The committed file carries the same history a reader fetches, plus the
	// baseline the next build diffs against.
	committed := committedFeed(t, s.ChangesPath)
	if len(committed.Records) != 2 || strings.Join(committed.Records[0].Added, ",") != "delta" {
		t.Errorf("committed records = %+v, want the same history that was published", committed.Records)
	}
	if _, ok := committed.State["delta"]; !ok {
		t.Error("the new entry is not in the baseline, so the next build would record it as added again")
	}
	if _, ok := committed.State["gamma"]; ok {
		t.Error("the removed entry is still in the baseline, so the next build would never see it leave")
	}
	if got := committed.State["beta"]["summary"]; got != "Second, rewritten." {
		t.Errorf("baseline summary = %v, want the value that was just published", got)
	}
}

// A build that changed nothing must leave the committed file alone, byte for
// byte. That is what lets a CI check assert the feed is in sync by asserting
// a clean diff, and what keeps a quiet directory from rewriting its own
// history once a day forever.
func TestABuildThatChangedNothingAppendsNoRecordAndRewritesNothing(t *testing.T) {
	s := feedSite(t, entry("alpha", "First."))
	renderTo(t, s)
	before, err := os.ReadFile(s.ChangesPath)
	if err != nil {
		t.Fatal(err)
	}

	renderTo(t, s)
	after, err := os.ReadFile(s.ChangesPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("a second identical build rewrote the committed feed")
	}
	if got := publishedFeed(t, renderTo(t, s)).Records; len(got) != 1 {
		t.Errorf("published %d records after three identical builds, want the bootstrap record alone", len(got))
	}
}

// The feed remembers thirty builds, newest first, and the oldest record is
// what gets dropped. A subscriber that was away for a while still reads the
// present end of the history, which is the end that tells it what to re-fetch.
func TestTheFeedKeepsThirtyBuildsAndDropsTheOldest(t *testing.T) {
	s := feedSite(t, entry("alpha", "v0"))
	base := s.GeneratedAt
	renderTo(t, s) // the bootstrap record

	for i := 1; i <= 31; i++ {
		s.Entries[0].Summary = fmt.Sprintf("v%d", i)
		s.GeneratedAt = base.Add(time.Duration(i) * time.Minute)
		renderTo(t, s)
	}

	got := committedFeed(t, s.ChangesPath)
	if len(got.Records) != changeFeedKeep {
		t.Fatalf("feed holds %d records, want %d", len(got.Records), changeFeedKeep)
	}
	oldest := base.Add(2 * time.Minute).Format(time.RFC3339)
	newest := base.Add(31 * time.Minute).Format(time.RFC3339)
	if got.Records[0].GeneratedAt != newest {
		t.Errorf("newest record = %s, want %s", got.Records[0].GeneratedAt, newest)
	}
	if got.Records[len(got.Records)-1].GeneratedAt != oldest {
		t.Errorf("oldest record = %s, want %s, the oldest kept rather than the oldest ever written",
			got.Records[len(got.Records)-1].GeneratedAt, oldest)
	}
	if published := publishedFeed(t, renderTo(t, s)); len(published.Records) != changeFeedKeep {
		t.Errorf("published %d records, want the same %d the feed keeps", len(published.Records), changeFeedKeep)
	}
}

// A probe moves status, last_ok, response_ms and last_checked on every sweep.
// Recording those would fill the feed with verdicts that are superseded by the
// next sweep — and status is already the live answer to the question the feed
// would be re-asking in slow motion.
func TestHealthVerdictsAreNotListingChanges(t *testing.T) {
	e := entry("alpha", "First.")
	e.MCPEndpointURL = strptr("https://alpha.example/mcp")
	s := feedSite(t, e)
	renderTo(t, s)
	before, err := os.ReadFile(s.ChangesPath)
	if err != nil {
		t.Fatal(err)
	}

	checked := time.Date(2026, 9, 28, 6, 0, 0, 0, time.UTC)
	s.HealthCheckedAt = &checked
	s.Unreachable = map[string]bool{"alpha": true}
	s.EndpointDetails = map[string]string{"alpha": "GET https://alpha.example/mcp = 404"}
	s.EndpointLastAlive = map[string]time.Time{"alpha": checked.Add(-time.Hour)}
	s.EndpointResponseMS = map[string]int{"alpha": 42}
	out := renderTo(t, s)

	after, err := os.ReadFile(s.ChangesPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("a service having a bad afternoon was recorded as a directory change")
	}
	if got := publishedFeed(t, out).Records; len(got) != 1 {
		t.Errorf("published %d records after a health change, want the bootstrap record alone", len(got))
	}
}

// The Pages build fetches values this repository does not own: a fresh agent
// feed, a fresh metrics count. Its diff would be against numbers the next
// snapshot build never sees, so it publishes what is committed and adds
// nothing — otherwise the feed would record a change that then un-happened.
func TestALiveBuildPublishesTheCommittedFeedWithoutAppending(t *testing.T) {
	s := feedSite(t, entry("alpha", "First."))
	renderTo(t, s)
	before, err := os.ReadFile(s.ChangesPath)
	if err != nil {
		t.Fatal(err)
	}

	s.RecordChanges = false
	s.Entries = []content.Entry{entry("alpha", "Rewritten from the live feed.")}
	out := renderTo(t, s)

	after, err := os.ReadFile(s.ChangesPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("a build that fetched fresh values wrote to the committed feed")
	}
	if got := publishedFeed(t, out).Records; len(got) != 1 {
		t.Errorf("published %d records, want the %d committed; a live build must not invent history", len(got), 1)
	}
}

// agents.json points at the feed, llms.txt names it in prose, and the feed
// itself says which directory it describes — an agent that arrived through
// either surface can find its way to the third.
func TestTheFeedIsLinkedFromTheSurfacesThatPointAtIt(t *testing.T) {
	s := feedSite(t, entry("vtessera", "A marketplace."))
	out := renderTo(t, s)
	const want = "https://agent-ai-tool.com/changes.json"

	if agents := read(t, out, "agents.json"); !strings.Contains(agents, `"changes": "`+want+`"`) {
		t.Error("agents.json does not link the change feed")
	}
	if llms := read(t, out, "llms.txt"); !strings.Contains(llms, want) {
		t.Error("llms.txt does not name the change feed")
	}

	got := publishedFeed(t, out)
	if got.SchemaVersion != 1 {
		t.Errorf("published schemaVersion = %d, want 1", got.SchemaVersion)
	}
	if got.Domain != "agent-ai-tool.com" {
		t.Errorf("published domain = %q", got.Domain)
	}
	if len(got.Records) != 1 {
		t.Errorf("published %d records, want the bootstrap record", len(got.Records))
	}
}

// No feed configured means no feed published. An empty file would read as
// "this directory has never changed", which is a different statement from "no
// feed is being kept" and not one this build is entitled to make.
func TestASiteWithoutAFeedPathPublishesNoFeed(t *testing.T) {
	out := renderTo(t, site(t, entry("vtessera", "A marketplace.")))

	if _, err := os.Stat(filepath.Join(out, "changes.json")); !os.IsNotExist(err) {
		t.Errorf("changes.json stat = %v, want it not to exist", err)
	}
}

// A feed written by a later version of this program is history this build
// does not know how to extend. Overwriting it would trade a detectable
// refusal for an undetectable gap.
func TestAFeedFromAnotherSchemaIsRefusedRatherThanOverwritten(t *testing.T) {
	s := feedSite(t, entry("alpha", "First."))
	foreign := []byte(`{"schemaVersion": 99, "records": [{"generatedAt": "2030-01-01T00:00:00Z", "added": ["someone-else"]}], "state": {}}` + "\n")
	if err := os.WriteFile(s.ChangesPath, foreign, 0o644); err != nil {
		t.Fatal(err)
	}

	err := s.Render(filepath.Join(t.TempDir(), "public"))
	if err == nil {
		t.Fatal("rendering against a feed from another schema should be refused")
	}
	if !strings.Contains(err.Error(), "schemaVersion") {
		t.Errorf("error = %v, want it to name the schema it refused", err)
	}
	after, readErr := os.ReadFile(s.ChangesPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(after, foreign) {
		t.Error("the refused feed was modified anyway")
	}
}
