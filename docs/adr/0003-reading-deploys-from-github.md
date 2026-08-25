# 3. Read deploy history from the GitHub Deployments API

Date: 2026-08-24

## Status

Accepted

## Context

The investigator could read alerts from PagerDuty and everything else from a JSON
file. That makes deploys, the single highest-prior event type in the model, the
one thing a responder had to assemble by hand before they could use the tool.
Exporting a JSON file during an incident is not something anyone will do.

The `Source` interface already exists for this. The question was which system to
read from.

## Decision

Read from the GitHub Deployments API, mapping each repository to a service name.

Deployments were chosen over Actions workflow runs because a workflow run is a CI
event and a deployment is a delivery event. A green workflow that never deployed
is not a candidate cause, and treating it as one puts noise at the top of the
list. The Deployments API also carries the environment, so an investigation of a
production incident is not polluted by staging activity.

Repositories map to services explicitly, as `owner/repo[=service]`. Repository
and service names are usually the same word and sometimes not, and the service
name has to match what alerts carry and what the dependency graph uses, or the
relevance scoring silently treats every deploy as unrelated.

The client is hand-rolled against two endpoints rather than pulling in the GitHub
SDK. Listing deployments and comparing two commits does not justify a dependency
that brings the rest of the API surface with it.

Magnitude comes from the diff between consecutive deployments of the same
environment, using the compare endpoint. The curve is logarithmic with a 200-file
change as the saturation point, because the difference between a one-file and a
ten-file deploy is much larger than between 100 and 110 files. Past a point a
deploy is simply big and the exact count stops carrying information.

## Consequences

`--github-repo acme/checkout` on the investigate command replaces the JSON file
for deploys, and the file source stays for replaying recorded incidents and for
event types GitHub does not know about.

Sizing costs one extra API call per deployment in the window. It is on by default
because magnitude is a quarter of the relevance blend and a deploy with no
magnitude is scored as merely average, but `--github-size-deploys=false` turns it
off for a rate-limited token or a wide investigation.

A failed comparison costs the event its magnitude and not its place on the
timeline. Losing a deploy from an investigation is far worse than losing one of
its four scoring signals, so the source degrades rather than fails.

A failed *listing* does fail the whole gather, which is deliberate and matches
`Gather`'s existing behaviour: an investigation built on partial data is
misleading in a way that is hard to notice, because the missing deploy looks
exactly like a quiet hour.

Rate limiting is named explicitly in the error rather than surfacing as "403
Forbidden", which sends people looking at token permissions instead of at the
clock.

This does not cover deploys that do not go through GitHub Deployments. Teams
deploying from Argo CD without recording a deployment, or from Spinnaker, need
their own adapter. The interface is the same, and the mapping and magnitude
concerns here apply to any of them.
