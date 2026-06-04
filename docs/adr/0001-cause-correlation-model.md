# 1. Score causes by time, service, type prior, and magnitude

Date: 2025-05-12

## Status

Accepted

## Context

When a page fires, the slow part of incident response is not usually fixing the
problem once you know where it is. It is finding where to look. In the last hour
there may be a dozen deploys, several config changes, autoscaling events, and a
cluster of related alerts. The responder has to guess which one matters and pull
on that thread first. A wrong guess costs minutes, and minutes are the whole
game for MTTR.

We want the tool to rank the candidate events by how well each one explains the
incident, and to show its reasoning so the responder can agree or overrule it.

## Decision

Score each candidate event with a model that combines four signals, then sort by
the score.

```
score = temporal(lead) * ( wS * serviceRelevance
                          + wT * typePrior
                          + wM * magnitude )
```

- **temporal(lead)** is a half-life decay, `0.5 ^ (lead / halfLife)`. An event at
  the moment of detection scores 1, one half-life earlier scores 0.5. This is a
  multiplier, not a weighted term, because a cause must precede its effect and a
  three-hour-old deploy is almost never the trigger no matter how relevant.
- **serviceRelevance** is 1 for the affected service and lower for others. A real
  dependency graph can raise the score of an upstream service's deploy; the
  default only knows exact matches.
- **typePrior** is the base rate of each event kind causing incidents. Deploys
  and config changes lead; ambient external events trail. These encode the field
  intuition "suspect the last deploy first."
- **magnitude** is a normalized signal strength (files changed, alert severity,
  size of a scaling change).

Events after detection or outside the lookback window are dropped. The score
breakdown is part of the output so the ranking is auditable, not a black box.

## Consequences

- A recent deploy to the affected service ranks above an older deploy elsewhere,
  and above a very recent but unrelated alert, which matches how an experienced
  responder triages.
- The weights and half-life are tunable per environment. They are documented
  defaults, not hidden constants.
- The model is correlational, not causal. It ranks plausibility and is explicit
  about that. It cannot know that a deploy was feature-flagged off, or that an
  "unrelated" service is actually upstream unless the dependency graph says so.
  The synthetic benchmark measures ranking quality (precision@1, MRR) so changes
  to the model can be evaluated rather than argued.
