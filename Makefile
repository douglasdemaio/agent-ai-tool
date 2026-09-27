TMPDIR ?= $(HOME)/.cache/go-tmp
export TMPDIR

DOMAIN ?= agent-ai-tool.com

.PHONY: all fmt vet test build generate refresh serve clean

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

serve: generate
	python3 -m http.server 8080 --directory public

clean:
	rm -rf bin public
