# 2. Score service relevance through a dependency graph

Date: 2026-08-24

## Status

Accepted

## Context

[ADR 0001](0001-cause-correlation-model.md) said service relevance was 1 for the
affected service and lower for everything else, and left "a real dependency
graph can raise the score of an upstream service's deploy" as an aspiration. The
shipped default matched service names exactly, which means the model was blind to
the second most common incident shape there is: the thing that broke is not the
thing that changed.

An auth deploy that breaks checkout produces a checkout page. The auth deploy is
sitting in the timeline, scored 0.10 on service relevance, ranked below an
unrelated deploy to a service nobody has thought about in a month. The responder
scrolls past the answer.

Measured on the synthetic benchmark, exact-service matching ranks the true cause
first 90% of the time when the cause is on the affected service, and 53% of the
time when it is one or two hops upstream. That gap is the problem.

## Decision

Add a `DependencyGraph` implementing the existing `ServiceGraph` interface,
loaded from a JSON map of service to the services it calls.

Relevance falls off with hop distance, and the two directions decay at different
rates:

- Toward a dependency (a service the affected one calls), 0.60 per hop. A direct
  dependency scores 0.60, a dependency of a dependency 0.36.
- Toward a consumer (a service that calls the affected one), 0.35 per hop.
- No path, or beyond three hops, falls back to 0.10.

The asymmetry is the substantive claim. If checkout calls auth, an auth change
breaking checkout is a direct causal path. The reverse happens too, when a
consumer changes its traffic pattern or starts retrying into its provider, but it
is rarer, so it scores lower rather than not at all.

Three hops is the cutoff because past that, in an estate of any size, almost
everything is connected to almost everything and the score stops carrying
information.

The graph also implements an optional `RelationExplainer`, so the score breakdown
prints "checkout depends on auth, 1 hop away" beside the number. Without it, a
responder sees 0.60 on a service that is not theirs and has no way to tell
whether the tool knows something they do not.

## Consequences

The benchmark now scores every trial twice, once with each model, and reports
the split by where the true cause was. At the default decay:

```
                          precision@1      MRR
exact-service match             0.809    0.895
dependency graph                0.813    0.899

precision@1 split by where the true cause was:
                           trials  exact-match      graph
same service                 2992        0.904      0.860
upstream dependency          1008        0.530      0.675
```

The aggregate barely moves, and that is the honest reading. The graph buys 14
points on upstream causes and gives back 4 on same-service ones, because noise
events on upstream services get lifted too. Whether that is a good trade depends
on how often the real cause is upstream, which is a property of an estate and not
of this model. `-upstream-decay` sweeps the exchange rate, and it is smooth and
monotonic across the range: at 0.40 upstream precision is 0.619 and same-service
is 0.879, at 0.70 they are 0.703 and 0.844.

The graph is optional and off by default. Without `--graph`, behaviour is exactly
what it was, which matters because a wrong graph is worse than no graph: it
confidently promotes the wrong service.

A stale graph degrades quietly. There is no signal in the output that the graph
is out of date, and a dependency added last week that nobody recorded is
invisible. Generating the file from a service mesh or a service catalog rather
than maintaining it by hand is the mitigation, and it is on the operator.

The model still has no notion of edge strength. A dependency called on every
request and one called on a nightly job are the same edge. Weighted edges are the
obvious next refinement, and the benchmark is the place to prove they help before
they ship.
