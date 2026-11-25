# 5. Chaos runs as candidate causes

Date: 2026-11-24

## Status

Accepted

## Context

chaos-operator injects faults on purpose. When a run pages someone, the
responder should learn that in the first minute and not after half an hour of
reading deploy diffs. The investigator could not see chaos runs, and the
portfolio map listed it as a loose end.

chaos-operator now keeps a record of each run in `status.runs` (its ADR 0004).
Each record has a stable ID, the target service, the start time, the outcome,
and whether a fault was injected.

## Decision

Add `ChaosSource` behind the `Source` interface. It reads a
ChaosExperimentList and turns each run record into a `chaos` event.

- The data comes from `kubectl get chaosexperiments -A -o json`, or from a
  saved copy of that output. kubectl already handles the responder's contexts,
  auth plugins and certificates. A client built on client-go would add a large
  dependency to reimplement that. The saved copy makes offline replay and tests
  work with the same parser.
- The event time is the run's start. That is when the fault began.
- Runs with `injected: false` are skipped. A run that found no pods, or that
  refused to start because the system was already unhealthy, cannot be the
  cause.
- The type prior is 0.95, the highest of any source. A deliberate fault on
  the paged service is the first thing to rule out.
- Magnitude comes from the outcome. `aborted` and `did_not_recover` score 1.0,
  because the run measurably hurt something. `succeeded` and `completed`
  score 0.6. An unknown outcome is neutral.
- The event ID is the run ID, such as `payments/nightly/20261124T030005Z`. It
  is the same ID chaos-operator writes, so a responder or a tool can look the
  run up directly.

## Consequences

- Timing still applies. A chaos run 15 minutes before the page ranks below a
  same-service deploy from 8 minutes before. That is the right call. Chaos does
  not make other changes impossible, and the higher prior only breaks close
  ties.
- The incident gym (platform-mcp, phase 2) creates incidents with chaos runs.
  If the investigator under test could read chaos runs, it would find the
  answer key. The gym has to turn this source off for the investigator and use
  the run IDs only for grading.
- `--chaos-from-cluster` fails when the CRD is not installed. A responder who
  asked for chaos runs should hear that they are not there, not get an empty
  list.
