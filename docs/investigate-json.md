# The `investigate --output json` format

`incident investigate --output json` prints one JSON document. platform-mcp
parses it, so this page is a contract. The golden file
[testdata/golden/investigate.json](../testdata/golden/investigate.json) pins
the exact output for the example incident.

## Fields

```json
{
  "schema": "incident-cli/investigate/v1",
  "incident": { "service": "checkout", "detected_at": "2026-01-15T12:00:00Z" },
  "window_seconds": 7200,
  "sources": ["file:testdata/incident-example.json", "chaos-runs"],
  "events": [
    {
      "id": "dep-checkout-4821",
      "source": "deploy",
      "service": "checkout",
      "title": "checkout v4821: refactor payment retry logic",
      "timestamp": "2026-01-15T11:52:00Z",
      "offset_seconds": -480,
      "magnitude": 0.7,
      "metadata": { "commit": "a1b2c3d", "files_changed": "14" }
    }
  ],
  "causes": [
    {
      "rank": 1,
      "event_id": "dep-checkout-4821",
      "score": 0.6184,
      "lead_time_seconds": 480,
      "components": {
        "temporal": 0.691,
        "service": 1,
        "type_prior": 0.9,
        "magnitude": 0.7,
        "relevance": 0.895,
        "service_relation": "same service"
      }
    }
  ],
  "total_candidates": 8
}
```

- `schema` names the version of this format.
- `events` holds every event the sources returned, in time order.
  `offset_seconds` is negative before detection.
- `causes` is the ranking, best first. `--top` limits it, and `--top 0` keeps
  every candidate. `total_candidates` always counts all of them.
- `event_id` in a cause is the `id` of an event in the same document. Tools
  use these IDs as evidence, so they must stay stable for the same input.
- `score` is `temporal` times `relevance`. `relevance` blends `service`,
  `type_prior` and `magnitude`. ADR 0001 explains the model.
- Scores are rounded to four decimal places.
- `metadata` and `service_relation` are left out when they are empty.

## Changing it

- Adding a field is fine. Readers must ignore fields they do not know.
- Renaming or removing a field, or changing what one means, needs a new
  `schema` value such as `incident-cli/investigate/v2`. Readers check
  `schema` and refuse a version they do not support.
- Regenerate the golden file with `go test ./cmd -run Golden -update`, and
  review the diff like a change to an API.
