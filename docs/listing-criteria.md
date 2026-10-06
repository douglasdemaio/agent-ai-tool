# Listing criteria

What an agent has to be true before this directory will draft a registration for
it on vtessera, and — just as important — which of those things the directory can
check and which only the agent's owner can.

The rule the criteria enforce is the one this repository already applies to its
own curated entries: *verify it yourself before you publish it*. A criterion
nobody can check is not a criterion here; it is a self-assertion, and it is
marked as one.

Each mechanically enforced rule lives in `internal/drafts` and is asserted by
`internal/drafts/drafts_test.go`, including negative cases, because a list nothing
can fail is documentation. `make drafts-verify` re-fetches the network evidence.

## The marketplace's own rules, quoted from where they are enforced

These are not opinions of this directory. `internal/registry/registry.go`
(`Register`) and `internal/domain/domain.go` (`AgentCard.validate`) refuse a
registration that misses them, and the refusal text is the specification:

| Rule | Enforced by |
|---|---|
| The agent id is a base58 Ed25519 public key, 32 bytes | `ParsePublicKey` |
| The card's `publicKey` must equal the agent id in the request path | `Register` |
| `name` is required, at most 200 characters | `AgentCard.validate` |
| `description` at most 2000 characters | `AgentCard.validate` |
| `url` is an absolute `http` or `https` URL with a host | `validateServiceURL` |
| `settlementModes` is `offchain` or `onchain` | `SettlementMode.Valid` |
| `currencies` are mints that decode to 32 bytes of base58 | `ValidateMint` |
| `probeTarget`, if present, is https with an explicit port and a path, no query, no fragment, no credentials | `validateProbeTarget` |
| At most 64 `capabilities` | `AgentCard.validate` |

## What the directory checks, and how

| Criterion | How it is checked |
|---|---|
| The agent publishes a card at a well-known path | `GET /.well-known/agent-card.json`, falling back to the legacy `/.well-known/agent.json`, must return 200 and parse as JSON with a `name` and a `url` |
| The declared service URL answers | The card's own `url` is fetched and must return 200 |
| The card and the service are the same operator's | The card URL and the service URL must both name the recorded host |
| The service the draft would register is the service that was checked | `card.url` must be byte-identical to the URL in the evidence |
| The agent names itself | Non-empty `name` |
| Name, description and capability count are within limits | Length checks, at the marketplace's limits |
| Every declared skill has an id and a name | Per-skill check |
| The agent declares at least one skill | At least one skill |
| The evidence can be re-checked later | The card's SHA-256 and the check time are recorded, and `make drafts-verify` re-fetches and compares |

A host that answers 404, 401, 405, 5xx or not at all fails. So does a card whose
service URL points at a `/.well-known/` path: a card that lists itself as its own
service is not describing an endpoint a buyer can call.

## What only the owner can supply

These are not criteria, and the directory does not pretend to check them. Each is
recorded per draft under `outstanding`, with the reason.

**`publicKey`.** This is the one that stops every draft from being sendable, and
it is worth being blunt about why: **an A2A agent card publishes no Ed25519
identity.** Across the 20 drafts, not one card declared a public key. The
marketplace derives the agent id from the authenticated session and requires the
card to name it, so a third party cannot complete a registration even in
principle. The owner generates a keypair and registers with it.

**`currencies` and `settlementModes`.** What an agent will accept for money is its
owner's commercial decision. The marketplace refuses a governed mint that has no
declared rate (`409 MINT_UNPRICED`) rather than guess one, which is the right
refusal and not something a directory should get in front of. The governed table
on mainnet-beta is USDC (`EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v`) and EURC
(`HzwqbKZw8HxMN6bF2yFZNrht3c2iXXzpKcFu7uBEDKtr`); `ValidateMint` refuses anything
else, including a lookalike address that decodes cleanly.

**`probeTarget`.** Must be https with an explicit port and a path. Only the owner
knows an endpoint that answers.

**`attestation`.** The card is signed by the agent's own key over the marketplace's
canonical `vtessera/attest/v1` form. The marketplace also accepts an unsigned card,
so this is optional for the owner — but an unsigned card cannot be published as
verified by anyone, which is the whole subject of the verification block.

## Two things the sweep turned up

Neither blocks a draft, and both are recorded in `evidence` rather than smoothed
over:

- **Most cards are served as `text/html`.** Of the drafts, the card path returns
  JSON with a `text/html` content type. The body parses, so the draft is sound,
  but a client that dispatches on content type will not find it. Recorded as
  `cardServedAsHTML`.
- **A2A and vtessera disagree about `capabilities`.** An A2A card declares
  capabilities as a transport feature object — `streaming`, `pushNotifications`,
  `stateTransitionHistory` — while the marketplace's card carries a list of
  strings. Translating one into the other would be inventing a claim about what an
  agent can do, so the drafts omit the field and preserve the A2A object verbatim
  under `evidence.a2aCapabilities` for the owner to translate if they wish.

## The drafts

Twenty, in `drafts/`, each one a real agent whose card was fetched and whose
service URL answered 200 at the time recorded in the file. Every one is
`status: awaiting-owner`, and `TestNoDraftClaimsToBeReadyToSend` fails if any of
them ever says otherwise.

A draft is a prepared request body, not a submission. Nothing here registers
anything: the directory does not hold any agent's key, and a registration made
with somebody else's identity would be refused by `requireOwnAgent` for good
reason.

### How they were found

Candidate hostnames came from a public index of agents discovered in certificate
transparency logs. That index was used **only as a list of candidates** — no claim
it makes about an agent, including its own assurance badge, was taken on trust.
Each host was then fetched and checked by this repository, and the ones that did
not publish a usable card or whose service URL did not answer were dropped. One
draft is a host that failed this way and was replaced rather than kept.

### Turning a draft into a registration

`make drafts-verify` re-checks all twenty against the live hosts, and this prints
the exact request for one of them, with what is still missing above it:

```bash
go run . -draft reviewchi
```

Which prints the `PUT /v1/agents/$AGENT_ID/card` request with the owner's own
variables, never a filled-in key:

```bash
curl -X PUT "$VTESSERA_BASE_URL/v1/agents/$AGENT_ID/card" \
  -H "Authorization: Bearer $VTESSERA_SESSION" \
  -H 'Content-Type: application/json' \
  --data @card.json
```

`AGENT_ID` and `VTESSERA_SESSION` come from the owner. The vtessera quickstart
covers generating the keypair and authenticating with it; nothing in this
repository holds, or can hold, an agent's signing key.
