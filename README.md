# incident-cli

A Go CLI tool that automates the mechanical parts of incident management: creating a PagerDuty incident, spinning up a dedicated Slack bridge channel, tracking MTTR from the moment of declaration, and generating a post-mortem template when the incident is resolved.

Built for on-call engineers who want to spend time diagnosing, not context-switching between tools.

## Features

- Declare an incident with one command — PagerDuty incident created, Slack channel bridged, MTTR timer started
- Resolve and auto-calculate MTTR; writes a timestamped event log
- Generate a prefilled post-mortem Markdown document from a template
- Configurable severity levels (P1–P4) mapped to PagerDuty urgencies

## Installation

```bash
go install github.com/jamespham/incident-cli@latest
```

Or build from source:

```bash
go build -o incident ./main.go
```

## Configuration

Set the following environment variables (or use a `.incident.yaml` config file):

```bash
export PAGERDUTY_API_KEY=your_api_key
export PAGERDUTY_SERVICE_ID=your_service_id
export SLACK_BOT_TOKEN=xoxb-your-token
export SLACK_INCIDENT_CHANNEL_PREFIX=inc
```

## Usage

```bash
# Declare a new incident
incident create --title "checkout API returning 503s" --severity P1

# List active incidents
incident list

# Resolve an incident and stop the MTTR timer
incident resolve --id INC-20250408-001

# Generate post-mortem document for a resolved incident
incident postmortem --id INC-20250408-001 --output postmortem.md
```

## Post-mortem template

The generated post-mortem includes:

- Timeline (auto-populated from event log)
- Impact summary
- Root cause analysis section
- Action items table
- Contributing factors

## Running tests

```bash
go test ./...
```

## License

MIT
