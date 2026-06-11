package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/jamespham/incident-cli/internal/pagerduty"
	"github.com/jamespham/incident-cli/internal/timeline"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var investigateCmd = &cobra.Command{
	Use:   "investigate",
	Short: "Reconstruct an incident timeline and rank the most likely causes",
	Long: `Gathers candidate events (deploys, config changes, scaling, related alerts)
from the configured sources within a lookback window, reconstructs the timeline,
and ranks which events most likely caused the incident.`,
	RunE: runInvestigate,
}

func init() {
	investigateCmd.Flags().String("service", "", "Affected service (required)")
	investigateCmd.Flags().String("at", "now", "Incident detection time (RFC3339 or 'now')")
	investigateCmd.Flags().Duration("window", 2*time.Hour, "Lookback window for candidate causes")
	investigateCmd.Flags().StringSlice("events", nil, "Path(s) to JSON event files")
	investigateCmd.Flags().Bool("pagerduty", false, "Also pull recent PagerDuty alerts as events")
	investigateCmd.Flags().Int("top", 3, "Number of candidate causes to highlight")
	_ = investigateCmd.MarkFlagRequired("service")
}

func runInvestigate(cmd *cobra.Command, args []string) error {
	service, _ := cmd.Flags().GetString("service")
	atStr, _ := cmd.Flags().GetString("at")
	window, _ := cmd.Flags().GetDuration("window")
	files, _ := cmd.Flags().GetStringSlice("events")
	usePD, _ := cmd.Flags().GetBool("pagerduty")
	top, _ := cmd.Flags().GetInt("top")

	at, err := parseAt(atStr)
	if err != nil {
		return fmt.Errorf("invalid --at: %w", err)
	}

	var sources []timeline.Source
	for _, f := range files {
		sources = append(sources, timeline.NewFileSource(f))
	}
	if usePD {
		sources = append(sources, timeline.NewPagerDutySource(
			pagerduty.NewClient(viper.GetString("PAGERDUTY_API_KEY"))))
	}
	if len(sources) == 0 {
		return fmt.Errorf("no event sources: pass --events <file> and/or --pagerduty")
	}

	events, err := timeline.Gather(at, window, sources...)
	if err != nil {
		return err
	}

	inc := timeline.Incident{ID: "investigation", Service: service, DetectedAt: at}
	ranked := timeline.NewCorrelator(timeline.DefaultConfig()).Rank(inc, events)

	printTimeline(inc, events)
	printCauses(ranked, top)
	return nil
}

func parseAt(s string) (time.Time, error) {
	if s == "" || s == "now" {
		return time.Now(), nil
	}
	return time.Parse(time.RFC3339, s)
}

func printTimeline(inc timeline.Incident, events []timeline.Event) {
	fmt.Printf("\nTimeline for %s incident (detected %s)\n",
		inc.Service, inc.DetectedAt.Format("2006-01-02 15:04:05"))
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for _, e := range timeline.Build(inc, events) {
		fmt.Fprintf(w, "  %s\t%s\t%s/%s\t%s\n",
			timeline.FormatOffset(e.Offset),
			e.Event.Timestamp.Format("15:04:05"),
			e.Event.Source, e.Event.Service,
			e.Event.Title,
		)
	}
	_ = w.Flush()
}

func printCauses(ranked []timeline.Candidate, top int) {
	fmt.Printf("\nMost likely causes:\n")
	if len(ranked) == 0 {
		fmt.Println("  no candidate events in the lookback window")
		return
	}
	if top > len(ranked) {
		top = len(ranked)
	}
	for i := 0; i < top; i++ {
		c := ranked[i]
		fmt.Printf("  %d. [%.2f] %s  (%s, %s, %s)\n",
			i+1, c.Score, c.Event.Title,
			c.Event.Source, c.Event.Service, timeline.FormatOffset(-c.LeadTime),
		)
		fmt.Printf("       time %.2f x relevance %.2f  (service %.2f, type %.2f, magnitude %.2f)\n",
			c.Components.Temporal, c.Components.Relevance,
			c.Components.Service, c.Components.TypePrior, c.Components.Magnitude,
		)
	}
}
