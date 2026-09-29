# /tmp is a tmpfs on the maintainer's machine and counts against RAM, so keep
# build scratch off it when a cache directory is available. CI has no such
# constraint and no such directory, and pointing Go at a missing TMPDIR makes
# it fail, so only set it when the directory really exists.
GO_TMP ?= $(HOME)/.cache/go-tmp
ifneq ($(wildcard $(GO_TMP)/.),)
export TMPDIR := $(GO_TMP)
endif

DOMAIN ?= agent-ai-tool.com

.PHONY: all fmt vet test build generate refresh check review serve clean

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

serve: generate
	python3 -m http.server 8080 --directory public

clean:
	rm -rf bin public
