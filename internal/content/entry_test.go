package content

import (
	"strings"
	"testing"
)

// A published call is a promise that this request works. These are the ways that
// promise can be made falsely.
func TestHowToCallValidation(t *testing.T) {
	ok := func() *HowToCall {
		return &HowToCall{Calls: []Call{{Name: "route", Method: "POST", Path: "/agp/route"}}}
	}
	cases := []struct {
		name  string
		build func() *HowToCall
		want  string
	}{
		{"no calls at all", func() *HowToCall { return &HowToCall{} }, "at least one call"},
		{"missing path", func() *HowToCall {
			return &HowToCall{Calls: []Call{{Name: "x", Method: "POST"}}}
		}, "path is required"},
		{"relative path", func() *HowToCall {
			return &HowToCall{Calls: []Call{{Name: "x", Method: "POST", Path: "agp/route"}}}
		}, "must start with /"},
		{"absolute path duplicates the host", func() *HowToCall {
			return &HowToCall{Calls: []Call{{Name: "x", Method: "POST", Path: "https://vtessera.fly.dev/agp/route"}}}
		}, "must be relative"},
		{"non-HTTP method", func() *HowToCall {
			return &HowToCall{Calls: []Call{{Name: "x", Method: "ROUTE", Path: "/agp/route"}}}
		}, "standard HTTP method"},
		{"GET with a body", func() *HowToCall {
			return &HowToCall{Calls: []Call{{Name: "x", Method: "GET", Path: "/v1/agents", Body: map[string]any{"a": 1}}}}
		}, "GET carries no request body"},
		{"same call twice", func() *HowToCall {
			return &HowToCall{Calls: []Call{
				{Name: "a", Method: "POST", Path: "/agp/route"},
				{Name: "b", Method: "POST", Path: "/agp/route"},
			}}
		}, "published twice"},
		{"unnamed call", func() *HowToCall {
			return &HowToCall{Calls: []Call{{Method: "POST", Path: "/agp/route"}}}
		}, "name is required"},
		{"auth with no type", func() *HowToCall {
			h := ok()
			h.Auth = &AuthFlow{Steps: []AuthStep{{Method: "POST", Path: "/v1/auth/challenge"}}}
			return h
		}, "auth.type is required"},
		{"auth with no steps", func() *HowToCall {
			h := ok()
			h.Auth = &AuthFlow{Type: "Ed25519"}
			return h
		}, "auth.steps must not be empty"},
		{"auth step with absolute path", func() *HowToCall {
			h := ok()
			h.Auth = &AuthFlow{Type: "Ed25519", Steps: []AuthStep{{
				Method: "POST", Path: "https://vtessera.fly.dev/v1/auth/challenge",
			}}}
			return h
		}, "must be relative"},
	}
	for _, c := range cases {
		err := c.build().validate()
		if err == nil {
			t.Errorf("%s: expected an error, got nil", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %q does not mention %q", c.name, err, c.want)
		}
	}
	if err := ok().validate(); err != nil {
		t.Errorf("a well-formed call was rejected: %v", err)
	}
}
