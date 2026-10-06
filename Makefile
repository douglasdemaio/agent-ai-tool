# /tmp is a tmpfs on the maintainer's machine and counts against RAM, so keep
# build scratch off it when a cache directory is available. CI has no such
# constraint and no such directory, and pointing Go at a missing TMPDIR makes
# it fail, so only set it when the directory really exists.
GO_TMP ?= $(HOME)/.cache/go-tmp
ifneq ($(wildcard $(GO_TMP)/.),)
export TMPDIR := $(GO_TMP)
endif

DOMAIN ?= agent-ai-tool.com

.PHONY: all fmt vet test build generate refresh check review commit-plan serve clean

all: fmt vet test build

fmt:
	gofmt -l -w .

vet:
	go vet ./...

test:
	go test ./...
	go test -race ./...

build:
	go build -o bin/agent-ai-tool .

# Generate the site into public/. With no VTESSERA_BASE_URL set the live
# section is absent, which is a supported state.
generate:
	go run . -domain $(DOMAIN)

# Fetch the live feeds and commit the snapshots. Needs a running service.
refresh:
	go run . -domain $(DOMAIN) -refresh

# Probe every advertised endpoint and write content/health.json. Exits zero
# whether or not endpoints are down; the verdict is the output, not the status.
check:
	go run . -domain $(DOMAIN) -check

# List curated entries overdue for human review, one per line. Also exits zero:
# an overdue entry is a reminder, not a broken build.
review:
	go run . -domain $(DOMAIN) -review

# Print commit or skip for the report in content/health.json against the one in
# git. The scheduled workflow reads this word; it is here so the decision can be
# inspected without reading the workflow.
#   make commit-plan PREVIOUS=previous-health.json
PREVIOUS ?=
commit-plan:
	go run . -domain $(DOMAIN) -commit-plan -previous-health-report $(PREVIOUS)

# Re-fetch every drafted registration's agent card and service URL. This is the
# only target here that talks to twenty hosts outside this repository, which is
# why it is not part of `all`: the hermetic suite stays hermetic, and this one
# fails when somebody else's server is down rather than when ours is.
drafts-verify:
	go test ./internal/drafts/ -run TestEveryDraftStillVerifiesOnTheWire -live -v

serve: generate
	python3 -m http.server 8080 --directory public

clean:
	rm -rf bin public
