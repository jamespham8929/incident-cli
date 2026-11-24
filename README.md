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
- **serviceRelevance** is highest for the affected service, and falls off with
  distance in a service dependency graph when you pass one. A service the
  affected one calls scores 0.60 at one hop. A service that calls it scores 0.35,
  because that direction causes incidents less often. Without `--graph` it is an
  exact-name match, as before.
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
       why service 1.00: same service
  2. [0.29] auth latency p99 above 800ms  (alert, auth, 2m00s before)
       time 0.91 x relevance 0.32  (service 0.10, type 0.50, magnitude 0.50)
       why service 0.10: different service, no graph loaded
  3. [0.17] search v2207: bump elasticsearch client  (deploy, search, 20m00s before)
       time 0.40 x relevance 0.44  (service 0.10, type 0.90, magnitude 0.50)
       why service 0.10: different service, no graph loaded
```

The recent `checkout` deploy ranks first even though the `auth` alert is more
recent, because the alert is on a different service and an alert is a weaker
cause signal than a deploy. That is the triage judgment the tool encodes, and the
score breakdown shows exactly why.

### Knowing what calls what

The thing that broke is often not the thing that changed. Pass a dependency graph
and an upstream change stops looking like noise:

```bash
./incident investigate \
  --service checkout \
  --at 2026-01-15T12:00:00Z \
  --events testdata/incident-example.json \
  --graph testdata/service-dependencies.json \
  --top 2
```

```
Most likely causes:
  1. [0.62] checkout v4821: refactor payment retry logic  (deploy, checkout, 8m00s before)
       time 0.69 x relevance 0.90  (service 1.00, type 0.90, magnitude 0.70)
       why service 1.00: same service
  2. [0.50] auth latency p99 above 800ms  (alert, auth, 2m00s before)
       time 0.91 x relevance 0.55  (service 0.60, type 0.50, magnitude 0.50)
       why service 0.60: checkout depends on auth, 1 hop away
```

The auth alert went from 0.29 to 0.50 because checkout calls auth. The graph is a
JSON map of each service to the services it calls:

```json
{
  "checkout": ["auth", "payments", "billing"],
  "payments": ["ledger", "auth"]
}
```

Relevance decays 0.60 per hop toward a dependency and 0.35 per hop toward a
consumer, stopping at three hops. The asymmetry is the point: auth breaking
checkout is the common story, checkout breaking auth is the rarer one.
[ADR 0002](docs/adr/0002-dependency-graph-relevance.md) has the reasoning and the
measured cost.

### Reading real deploy history

Deploys are the highest-prior event type in the model, and hand-assembling a JSON
file of them during an incident is not something anyone will do. Point the
investigator at GitHub instead:

```bash
export GITHUB_TOKEN=...
./incident investigate \
  --service checkout \
  --github-repo acme/checkout-service=checkout \
  --github-repo acme/auth \
  --github-env production \
  --graph deps.json \
  --pagerduty
```

Each repo maps to a service name, because the name has to match what alerts carry
and what the graph uses. Deploy magnitude comes from the diff between consecutive
deployments, so a 14-file change outranks a one-line config bump. Turn that off
with `--github-size-deploys=false` if the extra API call per deploy is a problem.

### Finding what nobody recorded

Some causes are never deployed or flagged. A connection pool fills up, an
upstream gets slow, pods start restarting. Point the investigator at Prometheus
and a file of metrics to watch, and a sustained shift in any of them becomes a
candidate cause at the moment it started:

```bash
./incident investigate \
  --service checkout \
  --prometheus-url http://prometheus:9090 \
  --anomaly-queries testdata/anomaly-queries.json \
  --graph deps.json
```

```
  12m00s before  09:48:00  metric_anomaly/payments-db  db_pool_wait_seconds rose to 0.9 from a baseline of 0.01
```

The baseline is the 6 hours before the window, scored with the median and the
median absolute deviation, so an old spike does not hide a new shift. A shift
has to hold for three minutes. Watch saturation and dependency signals, not the
SLI that paged, because a shift in the paging SLI is the incident itself.
[ADR 0004](docs/adr/0004-metric-anomalies-as-candidate-causes.md) covers the
detector and the prior.

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
  incident, or for event types nothing else exports.
- **PagerDutySource** turns recent PagerDuty incidents into alert events.
- **GitHubDeploymentSource** reads the GitHub Deployments API, one repo per
  service, and sizes each deploy from its diff.
  [ADR 0003](docs/adr/0003-reading-deploys-from-github.md) covers why deployments
  rather than workflow runs.
- **PrometheusAnomalySource** runs range queries and turns sustained metric
  shifts into `metric_anomaly` events.

Argo CD and Spinnaker adapters slot in behind the same interface.

## Benchmark

Ranking quality is measured on synthetic incidents whose true cause is known by
construction. Each trial plants one true cause and surrounds it with noise. Some
causes are a change to the affected service, the rest are a change to a service
it depends on, which is the case exact matching cannot rank. Every trial is
scored twice, once with each model, so the graph's effect is measured rather than
claimed.

```bash
go run ./benchmarks/correlator -trials 4000 -noise 12 -upstream 0.35
```

```
synthetic incidents: 4000, noise events each: 12, upstream causes: 35%, seed: 7
graph decay: 0.60 per hop upstream, 0.35 per hop downstream

                          precision@1      MRR
exact-service match             0.809    0.895
dependency graph                0.813    0.899

precision@1 split by where the true cause was:
                           trials  exact-match      graph
same service                 2992        0.904      0.860
upstream dependency          1008        0.530      0.675
```

`precision@1` is the fraction of incidents where the true cause ranked first.
`MRR` is the mean reciprocal rank, where 1.0 is perfect.

The aggregate barely moves, and that is the honest reading. The graph buys 14
points on upstream causes and gives back 4 on same-service ones, because noise
on upstream services gets lifted too. Whether that trade is worth making depends
on how often the real cause is upstream in your estate, which is not something
this model can know. `-upstream-decay` sweeps the exchange rate, and it is smooth
across the range: at 0.40 upstream precision is 0.619 and same-service is 0.879,
at 0.70 they are 0.703 and 0.844.

Runs are deterministic for a given `-seed`, so the numbers reproduce. Raise
`-noise` to watch ranking degrade as the hour gets busier.

## Tests

```bash
go test ./...
go test ./cmd -run Golden -update   # after a deliberate change to the output
```

The correlator tests in [internal/timeline/correlator_test.go](internal/timeline/correlator_test.go)
encode the triage rules directly: a recent same-service deploy must outrank an
old unrelated one, an event after detection is never a cause, and the score is
the product of the temporal and relevance factors.
[internal/timeline/graph_test.go](internal/timeline/graph_test.go) covers hop
distance, direction, cycles, and the case the whole feature exists for: two
identical deploys at the same moment, one of them upstream, ranked apart only
because the graph knows the difference.

The rendered output is checked against golden files in
[testdata/golden/](testdata/golden/). It is the product here: a responder reads
it at 3am and decides what to do next, so a change to its shape should show up as
a diff in review rather than being noticed later.

## Configuration

PagerDuty and Slack credentials come from environment variables or `~/.incident.yaml`:

```bash
export PAGERDUTY_API_KEY=...
export PAGERDUTY_SERVICE_ID=...
export SLACK_BOT_TOKEN=xoxb-...
export GITHUB_TOKEN=...           # for --github-repo
```

## License

MIT
