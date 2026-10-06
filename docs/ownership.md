# Ownership

Who owns each thing this repository publishes, how that changes hands, and which
pieces of ownership cannot be handed over by writing them down.

The short version: **this repository is not shared-owned today.** One maintainer
holds `contents: write` on four scheduled workflows and can publish anything.
This document says so, says what would have to change, and gives the handover
runbook for whoever does it. A shared-ownership document that describes a shared
setup which does not exist is worse than none, because it reads as a control.

## What is owned, and by whom

| Thing | Where | Who may change it | If they stop |
|---|---|---|---|
| Curated entries | `content/entries/*.json` | Any maintainer who adds one | The entry ages into the review queue; the monthly issue lists it |
| Live snapshots | `content/live-*.json` | Nobody, by hand — a scheduled workflow commits them | The snapshot goes stale and the site says so with its age |
| Endpoint health | `content/health.json` | The `check-health` workflow | Same: an unreachable endpoint is withheld, not published |
| Drafted registrations | `drafts/*.json` | Any maintainer | They are drafts; nothing depends on them continuing to exist |
| Verification verdicts | `internal/live`, `internal/render` | Whoever changes the verifier | The worst case is a wrong claim published as verified, so the verifier is reviewed hardest |
| The published site | GitHub Pages | A merge to `main` | The last good build keeps serving |

Two rows depend on work that is open but not merged, and are listed here so the
table describes the repository as it is going to be rather than as `main` is
today: **drafted registrations** come from #2 (`drafts/`, `internal/drafts`),
and **verification verdicts** from #1 (`internal/live/verify.go`,
`internal/render`). If either is declined, those two rows go away with it.

Two rows are deliberately not in the maintainer's column:

- **An agent's own registration.** The directory drafts it; the agent's owner
  completes it with that owner's own key. Nobody here can register on an agent's
  behalf, and `requireOwnAgent` in vtessera refuses any attempt, for good reason.
- **The vtessera marketplace identity.** Its signing key lives on a volume in a
  separate repository and deployment. It is not an artefact of this one, and
  nothing here can rotate, replace, or recover it. See below.

## The claim has an owner

This is the part the rest of the machinery serves. A listing on this site is a
claim that something is real, answers, and says what the summary says. Claims
need someone answerable for them, and the answerable person is the one who added
the entry.

So:

- Adding an entry means owning its truth until `last_verified` says otherwise.
  Not owning the code, not owning the deployment — the claim.
- Not bumping `last_verified` without checking. It is the only record that a human
  looked; bumping it unread makes the field a lie and the mechanism pointless.
- When an owner stops being available, the entry does **not** get deleted
  automatically. It ages, the monthly issue lists it, and a decision to keep or
  drop it gets made deliberately. An unattended claim should become visible, not
  silently vanish.
- Drafted registrations have no claim owner here, because the directory publishes
  no claim about them. They record what was checked and when. The claim that an
  agent offers a service belongs to whoever owns that agent.

## What is shared today, precisely

Sharing in the ordinary sense — anyone can open a pull request — yes. The
mechanical controls that make it more than that:

- `gofmt`, `go vet`, `go test ./...` and `-race` all run in `make`, and CI is
  green on the branch. These need no human to be trusted.
- Every mechanically enforced claim is asserted with negative cases, so the
  criteria cannot rot into decoration.
- The scheduled workflows commit snapshots and open the monthly review issue
  without anyone remembering.
- `make drafts-verify` re-checks all twenty drafts against the live hosts.

What is **not** shared: the ability to publish. One person can merge to `main`,
one person can push to the Pages branch, one person can change what the
verification block claims. There is no `CODEOWNERS` file, no branch protection
requiring review, and no second pair of eyes on the code that decides whether a
signature verifies.

That is a single point of failure with a single point of honesty, and it should be
recorded rather than described as a distributed arrangement.

## What would have to change

In rough order of how much they buy:

1. **Branch protection on `main`**: require a review, and require CI to pass.
   The cheapest real change, and the one that makes a second maintainer's
   approval mean something.
2. **`CODEOWNERS`** requiring review on `internal/live/verify.go` and
   `internal/drafts/`. Both decide what the site will assert is true. Everything
   else is plumbing.
3. **Two maintainers on the release path**, so a deploy is never one person's
   afternoon.
4. **A documented rotation** for whoever currently holds the credentials below,
   including who to tell when they change.

Items 1 and 2 are what changes the answer to "who owns this". Items 3 and 4 are
what make an answer survive the person who gave it.

## Credentials, and the one that cannot move

| Secret | Where it lives | If it leaks | If it is lost |
|---|---|---|---|
| `VTESSERA_BASE_URL` | repository secret | Not sensitive; it is a public URL | Re-set it |
| GitHub Pages deploy token | GitHub, per repository | Revoke in GitHub settings | Re-enable Pages |
| **`vtessera` marketplace signing key** | **a volume in the vtessera deployment, not here** | **A new key is a new identity: every card and receipt already issued stops verifying** | **A new deployment. Not a recovery.** |

The third row is the one to read twice. It is not in this repository and is not
described by this repository. It was created on first boot of that deployment and
cannot be regenerated: a new key means a new marketplace, and the previous one's
history does not carry over. Recovery is volume snapshots, not a rebuild.

Practically, this means:

- Do not delete or rebuild that volume to "reset" anything.
- If a key is ever exposed, treat the marketplace as compromised and rotate
  deliberately, knowing what breaks.
- Any change to that deployment belongs to whoever owns vtessera, not to whoever
  owns this directory. The dependency is one-directional and read-only from here.

## Handover runbook

For whoever is taking this over, or taking it over from you:

1. **Repository access.** Add them as a maintainer. Do not share an account; the
   `last_verified` field and the review cadence both assume a person can be named.
2. **Turn on branch protection** before they push anything, so their first change
   goes through review like everyone else's.
3. **Show them the two claims that are easy to get wrong.** The verification block
   checks signatures against a key from `/healthz` and deliberately ignores the
   marketplace's own `valid` flags; and a report that could not be fetched must
   read as `unavailable`, never as `unsigned`. Both have tests, and both are
   commented at the point where somebody would be tempted to simplify them.
4. **Walk the monthly review.** `make review`, then one entry checked end to end
   against the live service. If they can do that unassisted, the handover has
   worked; if not, the mechanism is not as self-describing as this document
   claims.
5. **Record the rotation.** Repository secrets are listed above. The vtessera key
   is not in this repository, by design, and its owner needs to know that this
   directory depends on it and cannot repair it.

## When a listing is wrong

Deleting the file retires the entry. The health check already withholds an
endpoint that has stopped answering, so a temporarily-down service needs nothing
— it returns when it returns, which is exactly the difference between *down* and
*gone*.

For an agent's own registration, that is the vtessera owner's call and not this
directory's: `POST /v1/admin/agents/{id}/retire`, gated on the marketplace's own
admin token. Nothing here can retire an agent, and nothing here should try.
