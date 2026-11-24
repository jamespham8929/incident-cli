# 4. Metric anomalies as candidate causes

Date: 2026-11-23

## Status

Accepted

## Context

The investigator ranked deploys, config changes, flags, scaling, and alerts. All
of them are events someone or something recorded on purpose. Many incidents
start with something nobody recorded: a connection pool fills up, an upstream
gets slow, pods start restarting. Those show up in metrics minutes before the
page, and the responder finds them by scrolling dashboards.

Prometheus already holds those signals. The question was how to turn a time
series into an event the correlator can rank.

## Decision

Add `PrometheusAnomalySource` behind the existing `Source` interface. For each
configured query, it pulls the lookback window plus a baseline before it (6
hours by default) and looks for a sustained shift.

- The baseline uses the median and the median absolute deviation, not the mean
  and standard deviation. An earlier spike in the baseline would inflate a
  standard deviation and hide the real shift. The median ignores it.
- A shift has to hold for 3 points in a row (3 minutes at the default step)
  beyond a robust z-score of 4. One bad scrape is noise. A change that holds is
  a candidate.
- The event's time is the onset of the shift, not its peak. Timing is the
  strongest factor in the score, and the onset is when the cause began.
- Magnitude grows with the z-score and saturates at three times the threshold.
- Each query names the label that carries the service, so the dependency graph
  can lift a shift in an upstream service over an unrelated deploy.

The type prior is 0.55. That is above a related alert, because a shift in a
dependency often is the cause seen from the side. It is below every deliberate
change, because a metric shift can also be one more symptom.

## Consequences

- Queries have to point at saturation and dependency signals, not at the SLI
  that paged. A shift in the paging SLI is the incident itself, and it would
  rank first for the wrong reason. The example file shows the intended kind.
- A flat baseline, like an error count that is always zero, has a MAD of zero.
  A small floor keeps the score finite, so errors where there were none still
  count as a large shift.
- Seasonal metrics can look shifted against a 6 hour baseline, for example the
  morning traffic ramp. `--anomaly-baseline` and `--anomaly-threshold` tune
  this. A seasonal baseline (same hour last week) is the next step if it is
  noisy in practice.
