# incident-cli

A Go CLI for on-call engineers. It handles the mechanical parts of incident
management (declare in PagerDuty, open a Slack bridge, track MTTR, generate a
post-mortem), and adds the part most tooling skips: **it reconstructs the
incident timeline and ranks the events most likely to have caused it.**

## The problem worth solving

Fixing an incident is usually fast once you know where to look. Finding where to
look is the slow part. When a service pages at 2am, the last hour holds a dozen
deploys, a handful of config changes, some autoscaling events, and a cluster of
related alerts. The responder has to guess which one matters and pull that thread
first. A wrong guess burns minutes, and minutes are the whole game for MTTR.

`incident investigate` does that triage. It gathers candidate events from your
sources, reconstructs the timeline around the incident, and ranks the likely
causes, showing its reasoning so you can agree or overrule it.

## How the ranking works

Each candidate event is scored by combining four signals (full reasoning in
[ADR 0001](docs/adr/0001-cause-correlation-model.md)):

```
score = temporal(lead) * ( 0.45*serviceRelevance + 0.30*typePrior + 0.25*magnitude )
```

- **temporal** is a half-life decay on how long before the incident the event
  happened. It is a multiplier, because a cause has to precede its effect and a
  three-hour-old deploy is almost never the trigger.
- **serviceRelevance** is highest for the affected service. A dependency graph
  can raise an upstream service's score; the default matches exact services.
- **typePrior** is the base rate of each event kind causing incidents: deploys
  and config changes lead, ambient external events trail.
- **magnitude** is a normalized signal strength (files changed, alert severity).

Events after detection or outside the lookback window are dropped. The score
breakdown is printed with each candidate, so the ranking is auditable.

## Usage

```bash
go build -o incident .

# Investigate a checkout incident using a recorded event file
./incident investigate \
  --service checkout \
  --at 2026-01-15T12:00:00Z \
  --events testdata/incident-example.json \
  --window 2h --top 3
```

```
Timeline for checkout incident (detected 2026-01-15 12:00:00)
  1h10m before   10:50:00  config_change/checkout  checkout: raise DB connection pool to 200
  40m00s before  11:20:00  feature_flag/catalog    catalog: enabled new ranking experiment
  30m00s before  11:30:00  scaling/billing         billing scaled 6 to 3 replicas (off-peak)
  20m00s before  11:40:00  deploy/search           search v2207: bump elasticsearch client
  8m00s before   11:52:00  deploy/checkout         checkout v4821: refactor payment retry logic
  2m00s before   11:58:00  alert/auth              auth latency p99 above 800ms

Most likely causes:
  1. [0.62] checkout v4821: refactor payment retry logic  (deploy, checkout, 8m00s before)
       time 0.69 x relevance 0.90  (service 1.00, type 0.90, magnitude 0.70)
  2. [0.29] auth latency p99 above 800ms  (alert, auth, 2m00s before)
       time 0.91 x relevance 0.32  (service 0.10, type 0.50, magnitude 0.50)
  3. [0.17] search v2207: bump elasticsearch client  (deploy, search, 20m00s before)
       time 0.40 x relevance 0.44  (service 0.10, type 0.90, magnitude 0.50)
```

The recent `checkout` deploy ranks first even though the `auth` alert is more
recent, because the alert is on a different service and an alert is a weaker
cause signal than a deploy. That is the triage judgment the tool encodes, and the
score breakdown shows exactly why.

Pull live alerts in as events too:

```bash
export PAGERDUTY_API_KEY=...
./incident investigate --service checkout --pagerduty --events deploys.json
```

### The rest of the lifecycle

```bash
./incident create --title "checkout 503s" --severity P1   # PD incident + Slack bridge + MTTR timer
./incident list --since 24h                                # recent incidents
./incident resolve --id INC-123 --resolution "rolled back v4821"
./incident postmortem --id INC-123 --output postmortem.md
```

## Event sources

Anything that satisfies the `Source` interface can feed the investigator:

```go
type Source interface {
    Name() string
    Fetch(window time.Duration, before time.Time) ([]Event, error)
}
```

Shipped sources:

- **FileSource** reads a JSON array of events. Use it to replay a recorded
  incident or to feed deploy history exported from your CD system.
- **PagerDutySource** turns recent PagerDuty incidents into alert events.

A deploy source backed by Argo CD, GitHub Deployments, or Spinnaker is the
obvious next adapter and slots in behind the same interface.

## Benchmark

Ranking quality is measured on synthetic incidents whose true cause is known by
construction. Each trial plants one true cause (a recent deploy or config change
to the affected service) and surrounds it with noise, then checks where the true
cause ranks.

```bash
go run ./benchmarks/correlator -trials 2000 -noise 12
```

```
synthetic incidents: 2000, noise events each: 12, seed: 7
precision@1: 0.842
MRR:         0.917
```

`precision@1` is the fraction of incidents where the true cause ranked first;
`MRR` is the mean reciprocal rank (1.0 is perfect). So on a busy hour with twelve
unrelated events surrounding the real one, the true cause is ranked first about
84% of the time and sits in the top two almost always. The run is deterministic
for a given `-seed`, so the numbers reproduce. Raise `-noise` to see ranking
degrade as the hour gets busier.

## Tests

```bash
go test ./...
```

The correlator tests in [internal/timeline/correlator_test.go](internal/timeline/correlator_test.go)
encode the triage rules directly: a recent same-service deploy must outrank an
old unrelated one, an event after detection is never a cause, and the score is
the product of the temporal and relevance factors.

## Configuration

PagerDuty and Slack credentials come from environment variables or `~/.incident.yaml`:

```bash
export PAGERDUTY_API_KEY=...
export PAGERDUTY_SERVICE_ID=...
export SLACK_BOT_TOKEN=xoxb-...
```

## License

MIT
