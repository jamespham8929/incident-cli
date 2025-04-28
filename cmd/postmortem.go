package cmd

import (
	"fmt"
	"os"
	"text/template"
	"time"

	"github.com/jamespham/incident-cli/internal/pagerduty"
	"github.com/jamespham/incident-cli/internal/timer"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var postmortemCmd = &cobra.Command{
	Use:   "postmortem",
	Short: "Generate a post-mortem document for a resolved incident",
	RunE:  runPostmortem,
}

func init() {
	postmortemCmd.Flags().String("id", "", "Incident ID (required)")
	postmortemCmd.Flags().String("output", "", "Output file path (default: stdout)")
	postmortemCmd.Flags().String("template", "", "Custom template file (optional)")
	_ = postmortemCmd.MarkFlagRequired("id")
}

type postmortemData struct {
	IncidentID   string
	Title        string
	Severity     string
	DeclaredAt   string
	ResolvedAt   string
	MTTR         string
	Date         string
	Author       string
}

const defaultTemplate = `# Post-Mortem: {{ .Title }}

**Incident ID:** {{ .IncidentID }}
**Severity:** {{ .Severity }}
**Date:** {{ .Date }}
**Author:** {{ .Author }}

---

## Summary

<!-- 2-3 sentences describing what happened and who was affected. -->

## Timeline

| Time (UTC) | Event |
|------------|-------|
| {{ .DeclaredAt }} | Incident declared |
| {{ .ResolvedAt }} | Incident resolved |

**MTTR:** {{ .MTTR }}

## Root cause

<!-- What was the technical root cause? -->

## Contributing factors

<!-- What conditions made this incident more likely or harder to diagnose? -->

## Impact

- **User-facing:**
- **Internal systems:**
- **Data loss:** None / See below

## Detection

<!-- How was the incident detected? Alert, customer report, internal monitoring? -->
<!-- How long between when the issue started and when it was detected? -->

## Resolution

<!-- What steps resolved the incident? -->

## Action items

| Action | Owner | Due date | Priority |
|--------|-------|----------|----------|
| | | | |

## Lessons learned

<!-- What went well? What went poorly? What should change? -->
`

func runPostmortem(cmd *cobra.Command, args []string) error {
	id, _ := cmd.Flags().GetString("id")
	output, _ := cmd.Flags().GetString("output")
	templateFile, _ := cmd.Flags().GetString("template")

	pdClient := pagerduty.NewClient(viper.GetString("PAGERDUTY_API_KEY"))
	t := timer.NewMTTRTimer()
	t.Load(id)

	incident, err := pdClient.GetIncident(id)
	if err != nil {
		return fmt.Errorf("fetching incident details: %w", err)
	}

	data := postmortemData{
		IncidentID: id,
		Title:      incident.Title,
		Severity:   incident.Severity,
		DeclaredAt: incident.CreatedAt.UTC().Format("2006-01-02 15:04:05"),
		ResolvedAt: incident.ResolvedAt.UTC().Format("2006-01-02 15:04:05"),
		MTTR:       formatDuration(t.Elapsed(id)),
		Date:       time.Now().Format("2006-01-02"),
		Author:     os.Getenv("USER"),
	}

	tmplStr := defaultTemplate
	if templateFile != "" {
		b, err := os.ReadFile(templateFile)
		if err != nil {
			return fmt.Errorf("reading template file: %w", err)
		}
		tmplStr = string(b)
	}

	tmpl, err := template.New("postmortem").Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("parsing template: %w", err)
	}

	out := os.Stdout
	if output != "" {
		f, err := os.Create(output)
		if err != nil {
			return fmt.Errorf("creating output file: %w", err)
		}
		defer f.Close()
		out = f
	}

	return tmpl.Execute(out, data)
}
