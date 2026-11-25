# 6. Symptoms and cascades

Date: 2026-11-25

## Status

Accepted

## Context

platform-mcp's incident gym breaks a real shop on kind and records what
Prometheus saw. The shop is a call chain: frontend calls checkout, which calls
inventory. The gym pages on frontend. In the first five recordings, the
correlator never ranked the cause in its top three. It had scored 0.810 top-1
on the synthetic benchmark.

The rankings showed three problems.

- The page's own symptoms ranked first. frontend's error ratio is an anomaly
  on the paged service, so it gets a full service score. ADR 0004 says to
  point anomaly queries away from the SLI that paged. But one query file
  watches every service, and any service can page, so the paged SLI is always
  in it.
- A fault spreads up the call chain within one minute. At one-minute steps,
  every anomaly in a cascade has the same onset. Timing cannot tell the cause
  from its symptoms.
- The graph made it worse. It scores a service lower for each hop from the
  page. That is right for a deploy, which is less likely to matter the further
  away it is. It is wrong for a cascade, which starts at the furthest service
  that broke.

## Decision

Add a cascade pass that runs after scoring, when the graph can list each
service's dependencies and the paged service has a health anomaly.

- Anomaly queries get a `kind`. `health` is the default, for errors, latency
  and capacity. `load` is for traffic. A traffic drop does not mean a service
  is broken. A failure upstream of it cuts its traffic too.
- The pass walks from the paged service to its dependencies, and stays on
  services that have a health anomaly that started no later than the caller's,
  give or take one minute of slack. A dependency that broke later cannot
  explain its caller.
- A reached service whose dependencies are all fine is the frontier. The
  failure starts there.
- Events on a frontier service get a service relevance of 1. The chain of
  broken services between it and the page shows it matters, so the hop decay
  no longer applies.
- These events are scaled by `ExplainedFactor`, 0.5 by default:
  - events on a reached service that a broken dependency explains, including
    the page's own symptoms and its own deploys
  - a frontier anomaly when a change on the same service came before it, so
    the change ranks above the anomaly it explains
  - a change that came after its service's anomalies started
  - a load anomaly
- Each candidate reports the factor and the reason in the JSON output
  (`cascade` and `cascade_reason`) and in the text breakdown.

An explained event stays on the list. It moves below what explains it.

## Consequences

- On the five gym recordings, top-1 went from 0 of 5 to 5 of 5 with the
  graph. The rules were written after seeing those five incidents, so this
  shows the rules fix what the gym found. It does not show they generalize.
  The next gym scenarios are the test.
- Without a graph nothing changes. Without the graph, the gym still gets 0 of
  5, because there are no edges to follow.
- The synthetic benchmark has no metric anomalies, so its numbers did not
  move. CI checks that through platform-mcp's export diff.
- A dependency cycle leaves no frontier. Its services are only scaled down.
- The slack is one minute because anomalies come in one-minute steps. A
  slower cascade, such as a queue that takes minutes to back up, needs more.
- The pass trusts the graph. A missing edge hides a cascade, and a wrong
  edge can invent one. The graph was already a trade (ADR 0002), and this
  makes it a bigger one.
