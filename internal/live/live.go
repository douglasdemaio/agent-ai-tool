package live

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	DefaultTimeout = 20 * time.Second
	StaleAfter     = 14 * 24 * time.Hour
)

var ErrNoCache = errors.New("no committed snapshot")

type Validator func(body []byte) error

// Snapshot wraps the upstream response with the time it was taken. The age has
// to live in the file rather than be read from the file's mtime, because git
// does not preserve modification times: a fresh clone would report a
// year-old snapshot as minutes old and the banner would claim fresh data that
// is not.
type Snapshot struct {
	FetchedAt time.Time       `json:"fetchedAt"`
	Response  json.RawMessage `json:"response"`
}

type Result struct {
	Response  json.RawMessage
	FromCache bool
	FetchedAt time.Time
	FetchErr  error
}

func (r Result) Available() bool { return len(r.Response) > 0 }

func (r Result) Age(now time.Time) time.Duration {
	if !r.FromCache || r.FetchedAt.IsZero() {
		return 0
	}
	return now.Sub(r.FetchedAt)
}

func (r Result) Stale(now time.Time) bool {
	age := r.Age(now)
	return age > 0 && age > StaleAfter
}

func Load(ctx context.Context, baseURL, path, cachePath string, client *http.Client, validate Validator) Result {
	if baseURL != "" && client != nil {
		body, err := fetch(ctx, client, baseURL+path)
		if err == nil {
			if validate != nil {
				if err := validate(body); err != nil {
					err = fmt.Errorf("malformed response from %s%s: %w", baseURL, path, err)
				} else {
					return Result{Response: json.RawMessage(body)}
				}
			} else {
				return Result{Response: json.RawMessage(body)}
			}
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

func Refresh(ctx context.Context, baseURL, path, cachePath string, client *http.Client, validate Validator) error {
	if baseURL == "" {
		return fmt.Errorf("no base URL configured, refusing to write a snapshot")
	}
	body, err := fetch(ctx, client, baseURL+path)
	if err != nil {
		return err
	}
	if validate != nil {
		if err := validate(body); err != nil {
			return fmt.Errorf("refusing to cache a malformed response from %s%s: %w", baseURL, path, err)
		}
	}
	return WriteSnapshot(cachePath, json.RawMessage(body), time.Now().UTC())
}

func WriteSnapshot(cachePath string, response json.RawMessage, at time.Time) error {
	encoded, err := json.MarshalIndent(Snapshot{FetchedAt: at, Response: response}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(cachePath, append(encoded, '\n'), 0o644)
}

func readCache(cachePath string) (Snapshot, error) {
	var snapshot Snapshot
	raw, err := os.ReadFile(cachePath)
	if errors.Is(err, os.ErrNotExist) {
		return snapshot, ErrNoCache
	}
	if err != nil {
		return snapshot, err
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return snapshot, fmt.Errorf("%s is corrupt: %w", cachePath, err)
	}
	if len(snapshot.Response) == 0 {
		return snapshot, fmt.Errorf("%s has no response", cachePath)
	}
	return snapshot, nil
}

func fetch(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s = %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, fmt.Errorf("GET %s returned an empty body", url)
	}
	return body, nil
}
