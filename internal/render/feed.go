package render

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"time"
)

// changeFeedKeep is how many builds the feed remembers: more than a subscriber
// is likely to read, fewer than would make the file worth splitting, and the
// oldest record goes first so the feed always ends at the present.
const changeFeedKeep = 30

// changeFeedSchemaVersion is the shape of changes.json. It moves on a change
// of meaning, not on a change of directory, exactly as the schemaVersion in
// agents.json does.
const changeFeedSchemaVersion = 1

// healthFields are published in agents.json but are not listing changes. A
// probe moves them on every sweep: a service flapping between up and down
// would fill the feed with verdicts that are already superseded, and `status`
// is already the live answer a reader looks for. The feed records what the
// directory lists, not how the listings are doing.
var healthFields = map[string]bool{
	"status":                      true,
	"last_ok":                     true,
	"response_ms":                 true,
	"last_checked":                true,
	"endpoint_unreachable":        true,
	"endpoint_unreachable_reason": true,
	"endpoint_last_ok":            true,
}

// ChangeRecord is one build that moved the directory. It is the whole of what
// a subscriber needs to catch up without re-reading every entry: what arrived,
// what went, and what changed where.
type ChangeRecord struct {
	// GeneratedAt is when the build that noticed the change ran (RFC 3339).
	GeneratedAt string         `json:"generatedAt"`
	Added       []string       `json:"added,omitempty"`
	Removed     []string       `json:"removed,omitempty"`
	Changed     []ChangedEntry `json:"changed,omitempty"`
}

// ChangedEntry names an entry and the fields that moved on it. The field
// names are the names agents.json publishes, so a reader goes from the feed
// to the field with no translation table in between.
type ChangedEntry struct {
	Slug   string   `json:"slug"`
	Fields []string `json:"fields"`
}

// changeFeedFile is the committed form: the history a reader is offered, and
// the baseline this build diffed against. State is written because the diff
// needs somewhere to start and git history is not something a build may
// consult; it is not published, because a baseline is not news.
type changeFeedFile struct {
	SchemaVersion int                       `json:"schemaVersion"`
	Records       []ChangeRecord            `json:"records"`
	State         map[string]map[string]any `json:"state,omitempty"`
}

// publishedChangeFeed is what changes.json carries. It deliberately has no
// timestamp of its own: a build that changed nothing publishes the same bytes
// it published last time, which is what makes an ETag worth sending and what
// makes the committed file safe to diff.
type publishedChangeFeed struct {
	SchemaVersion int            `json:"schemaVersion"`
	Domain        string         `json:"domain"`
	Description   string         `json:"description"`
	Records       []ChangeRecord `json:"records"`
}

// listingState projects each entry onto the fields the feed watches: every
// field agents.json publishes, less the health verdicts. The projection is
// stored with the records so the next build compares against what was
// published rather than reconstructing what the directory used to look like.
func (s Site) listingState(views []entryView) (map[string]map[string]any, error) {
	state := make(map[string]map[string]any, len(views))
	entries := s.jsonEntries(views)
	for i, entry := range entries {
		body, err := json.Marshal(entry)
		if err != nil {
			return nil, fmt.Errorf("projecting %s for the change feed: %w", entry.Slug, err)
		}
		var fields map[string]any
		if err := json.Unmarshal(body, &fields); err != nil {
			return nil, fmt.Errorf("projecting %s for the change feed: %w", entry.Slug, err)
		}
		for name := range healthFields {
			delete(fields, name)
		}
		// The endpoints come from the entry, not from what agents.json is
		// currently willing to print: withholding is a health verdict, and
		// recording it would fill the feed with the same URL disappearing and
		// returning. The verdict itself is in status, which is not a listing
		// change either.
		if u := views[i].Entry.MCPEndpointURL; u != nil {
			fields["mcp_endpoint_url"] = *u
		} else {
			fields["mcp_endpoint_url"] = nil
		}
		if u := views[i].Entry.APIURL; u != nil {
			fields["api_url"] = *u
		} else {
			fields["api_url"] = nil
		}
		state[entry.Slug] = fields
	}
	return state, nil
}

// diffListings reports what moved between two states of the directory: which
// entries arrived, which left, and which stayed but published different
// fields. Every list comes back sorted, because the feed is committed output
// and a map's iteration order must never reach a file.
func diffListings(previous, current map[string]map[string]any) ([]string, []string, []ChangedEntry) {
	var added, removed []string
	var changed []ChangedEntry

	for slug := range current {
		if _, ok := previous[slug]; !ok {
			added = append(added, slug)
		}
	}
	for slug := range previous {
		if _, ok := current[slug]; !ok {
			removed = append(removed, slug)
		}
	}
	for slug, now := range current {
		before, ok := previous[slug]
		if !ok {
			continue
		}
		var fields []string
		for name, value := range now {
			was, present := before[name]
			if !present || !reflect.DeepEqual(was, value) {
				fields = append(fields, name)
			}
		}
		for name := range before {
			if _, present := now[name]; !present {
				fields = append(fields, name)
			}
		}
		if len(fields) > 0 {
			sort.Strings(fields)
			changed = append(changed, ChangedEntry{Slug: slug, Fields: fields})
		}
	}

	sort.Strings(added)
	sort.Strings(removed)
	sort.Slice(changed, func(i, j int) bool { return changed[i].Slug < changed[j].Slug })
	return added, removed, changed
}

// updateChangeFeed reads the committed feed, compares this build against it,
// and returns the records to publish.
//
// Only a build reading committed data writes. One that fetched values this
// repository does not own — a live metrics count, a fresh agent feed — would
// diff against numbers the next snapshot build never sees, and would record a
// change that then un-happened on the following run. Those builds publish what
// is committed and add nothing to it.
//
// The file is rewritten only when its bytes would differ, so a build that
// changed nothing leaves it untouched rather than re-stamping it: that is what
// lets a CI check assert the feed is in sync by asserting a clean diff.
func (s Site) updateChangeFeed(path string, views []entryView) ([]ChangeRecord, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var previous changeFeedFile
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &previous); err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		if previous.SchemaVersion != changeFeedSchemaVersion {
			return nil, fmt.Errorf("%s carries schemaVersion %d and this build writes %d; refusing to overwrite a history it does not understand",
				path, previous.SchemaVersion, changeFeedSchemaVersion)
		}
	}

	if !s.RecordChanges {
		return nonNilRecords(previous.Records), nil
	}

	current, err := s.listingState(views)
	if err != nil {
		return nil, err
	}
	records := previous.Records
	if added, removed, changed := diffListings(previous.State, current); len(added)+len(removed)+len(changed) > 0 {
		record := ChangeRecord{
			GeneratedAt: s.GeneratedAt.UTC().Format(time.RFC3339),
			Added:       added,
			Removed:     removed,
			Changed:     changed,
		}
		records = append([]ChangeRecord{record}, records...)
		if len(records) > changeFeedKeep {
			records = records[:changeFeedKeep]
		}
	}
	records = nonNilRecords(records)

	next := changeFeedFile{SchemaVersion: changeFeedSchemaVersion, Records: records, State: current}
	body, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding %s: %w", path, err)
	}
	body = append(body, '\n')
	if !bytes.Equal(body, raw) {
		if err := writeFile(path, body); err != nil {
			return nil, fmt.Errorf("writing %s: %w", path, err)
		}
	}
	return records, nil
}

// changeFeedJSON is the published form of the records.
func (s Site) changeFeedJSON(records []ChangeRecord) publishedChangeFeed {
	return publishedChangeFeed{
		SchemaVersion: changeFeedSchemaVersion,
		Domain:        s.Domain,
		Description:   fmt.Sprintf("What changed in this directory, newest first: the last %d builds that changed something, each naming the entries added, removed or changed and the fields that moved.", changeFeedKeep),
		Records:       nonNilRecords(records),
	}
}

func nonNilRecords(records []ChangeRecord) []ChangeRecord {
	if records == nil {
		return []ChangeRecord{}
	}
	return records
}
