package cmd

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/jamespham/incident-cli/internal/github"
	"github.com/jamespham/incident-cli/internal/pagerduty"
	"github.com/jamespham/incident-cli/internal/prometheus"
	"github.com/jamespham/incident-cli/internal/timeline"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func newInvestigateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "investigate",
		Short: "Reconstruct an incident timeline and rank the most likely causes",
		Long: `Gathers candidate events (deploys, config changes, scaling, related alerts)
from the configured sources within a lookback window, reconstructs the timeline,
and ranks which events most likely caused the incident.`,
		RunE: runInvestigate,
	}
	cmd.Flags().String("service", "", "Affected service (required)")
	cmd.Flags().String("at", "now", "Incident detection time (RFC3339 or 'now')")
	cmd.Flags().Duration("window", 2*time.Hour, "Lookback window for candidate causes")
	cmd.Flags().StringSlice("events", nil, "Path(s) to JSON event files")
	cmd.Flags().Bool("pagerduty", false, "Also pull recent PagerDuty alerts as events")
	cmd.Flags().StringSlice("github-repo", nil, "GitHub repo to read deploys from, as owner/repo[=service] (repeatable)")
	cmd.Flags().String("github-env", "production", "Deployment environment to read; empty for all")
	cmd.Flags().Bool("github-size-deploys", true, "Ask GitHub how much changed between deploys, for the magnitude signal")
	cmd.Flags().String("graph", "", "Service dependency graph JSON, so upstream changes rank as plausible causes")
	cmd.Flags().String("prometheus-url", "", "Prometheus to read metric anomalies from")
	cmd.Flags().String("anomaly-queries", "", "JSON file of metrics to watch for sustained shifts (needs --prometheus-url)")
	cmd.Flags().Duration("anomaly-baseline", timeline.DefaultAnomalyConfig().Baseline, "History before the window that counts as normal")
	cmd.Flags().Float64("anomaly-threshold", timeline.DefaultAnomalyConfig().Threshold, "Robust z-score a shift must pass")
	cmd.Flags().String("chaos-runs", "", "Saved `kubectl get chaosexperiments -A -o json` output to read chaos runs from")
	cmd.Flags().Bool("chaos-from-cluster", false, "Read chaos runs from the cluster with kubectl")
	cmd.Flags().String("kube-context", "", "kubeconfig context for --chaos-from-cluster; empty for the current one")
	cmd.Flags().Int("top", 3, "Number of candidate causes to highlight; 0 for all")
	cmd.Flags().String("output", "text", "Output format: text or json")
	_ = cmd.MarkFlagRequired("service")
	return cmd
}

func runInvestigate(cmd *cobra.Command, args []string) error {
	service, _ := cmd.Flags().GetString("service")
	atStr, _ := cmd.Flags().GetString("at")
	window, _ := cmd.Flags().GetDuration("window")
	files, _ := cmd.Flags().GetStringSlice("events")
	usePD, _ := cmd.Flags().GetBool("pagerduty")
	ghRepos, _ := cmd.Flags().GetStringSlice("github-repo")
	ghEnv, _ := cmd.Flags().GetString("github-env")
	ghSize, _ := cmd.Flags().GetBool("github-size-deploys")
	graphPath, _ := cmd.Flags().GetString("graph")
	promURL, _ := cmd.Flags().GetString("prometheus-url")
	anomalyPath, _ := cmd.Flags().GetString("anomaly-queries")
	anomalyBaseline, _ := cmd.Flags().GetDuration("anomaly-baseline")
	anomalyThreshold, _ := cmd.Flags().GetFloat64("anomaly-threshold")
	chaosPath, _ := cmd.Flags().GetString("chaos-runs")
	chaosCluster, _ := cmd.Flags().GetBool("chaos-from-cluster")
	kubeContext, _ := cmd.Flags().GetString("kube-context")
	top, _ := cmd.Flags().GetInt("top")
	output, _ := cmd.Flags().GetString("output")

	if output != "text" && output != "json" {
		return fmt.Errorf("--output must be text or json, not %q", output)
	}
	if top < 0 {
		return fmt.Errorf("--top must be 0 or more")
	}
	if chaosPath != "" && chaosCluster {
		return fmt.Errorf("--chaos-runs and --chaos-from-cluster are two ways to get the same data; pass one")
	}

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
	if len(ghRepos) > 0 {
		mappings, err := parseRepoMappings(ghRepos)
		if err != nil {
			return err
		}
		sources = append(sources, timeline.NewGitHubDeploymentSource(
			github.NewClient(viper.GetString("GITHUB_TOKEN")), mappings, ghEnv, ghSize))
	}
	if anomalyPath != "" || promURL != "" {
		if anomalyPath == "" || promURL == "" {
			return fmt.Errorf("--prometheus-url and --anomaly-queries go together")
		}
		queries, err := timeline.LoadAnomalyQueries(anomalyPath)
		if err != nil {
			return fmt.Errorf("loading anomaly queries: %w", err)
		}
		acfg := timeline.DefaultAnomalyConfig()
		acfg.Baseline = anomalyBaseline
		acfg.Threshold = anomalyThreshold
		sources = append(sources, timeline.NewPrometheusAnomalySource(
			prometheus.NewClient(promURL), queries, acfg))
	}
	if chaosPath != "" {
		sources = append(sources, timeline.NewChaosSource(timeline.ChaosFile{Path: chaosPath}))
	}
	if chaosCluster {
		sources = append(sources, timeline.NewChaosSource(timeline.ChaosKubectl{Context: kubeContext}))
	}
	if len(sources) == 0 {
		return fmt.Errorf("no event sources: pass --events <file>, --github-repo <owner/repo>, --prometheus-url with --anomaly-queries, --chaos-runs <file>, --chaos-from-cluster, and/or --pagerduty")
	}

	cfg := timeline.DefaultConfig()
	if graphPath != "" {
		graph, err := timeline.LoadDependencyGraph(graphPath, timeline.DefaultGraphConfig())
		if err != nil {
			return fmt.Errorf("loading dependency graph: %w", err)
		}
		cfg.Graph = graph
	}

	events, err := timeline.Gather(at, window, sources...)
	if err != nil {
		return err
	}

	inc := timeline.Incident{ID: "investigation", Service: service, DetectedAt: at}
	ranked := timeline.NewCorrelator(cfg).Rank(inc, events)

	if output == "json" {
		return writeInvestigationJSON(cmd.OutOrStdout(), inc, window, sources, events, ranked, top)
	}
	printTimeline(cmd.OutOrStdout(), inc, events)
	if top == 0 {
		top = len(ranked)
	}
	printCauses(cmd.OutOrStdout(), ranked, top)
	return nil
}

// parseRepoMappings turns the --github-repo flag values into mappings, failing
// on the first bad one rather than silently investigating fewer repos than the
// responder asked for.
func parseRepoMappings(specs []string) ([]timeline.RepoMapping, error) {
	mappings := make([]timeline.RepoMapping, 0, len(specs))
	for _, spec := range specs {
		mapping, err := timeline.ParseRepoMapping(spec)
		if err != nil {
			return nil, fmt.Errorf("invalid --github-repo: %w", err)
		}
		mappings = append(mappings, mapping)
	}
	return mappings, nil
}

func parseAt(s string) (time.Time, error) {
	if s == "" || s == "now" {
		return time.Now(), nil
	}
	return time.Parse(time.RFC3339, s)
}

func printTimeline(out io.Writer, inc timeline.Incident, events []timeline.Event) {
	fmt.Fprintf(out, "\nTimeline for %s incident (detected %s)\n",
		inc.Service, inc.DetectedAt.Format("2006-01-02 15:04:05"))
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
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

func printCauses(out io.Writer, ranked []timeline.Candidate, top int) {
	fmt.Fprintf(out, "\nMost likely causes:\n")
	if len(ranked) == 0 {
		fmt.Fprintln(out, "  no candidate events in the lookback window")
		return
	}
	if top > len(ranked) {
		top = len(ranked)
	}
	for i := 0; i < top; i++ {
		c := ranked[i]
		fmt.Fprintf(out, "  %d. [%.2f] %s  (%s, %s, %s)\n",
			i+1, c.Score, c.Event.Title,
			c.Event.Source, c.Event.Service, timeline.FormatOffset(-c.LeadTime),
		)
		fmt.Fprintf(out, "       time %.2f x relevance %.2f  (service %.2f, type %.2f, magnitude %.2f)\n",
			c.Components.Temporal, c.Components.Relevance,
			c.Components.Service, c.Components.TypePrior, c.Components.Magnitude,
		)
		// Without this line a responder sees a 0.36 service score on a service
		// that is not theirs and has no way to tell whether the tool knows
		// something they do not.
		if c.Components.ServiceRelation != "" {
			fmt.Fprintf(out, "       why service %.2f: %s\n", c.Components.Service, c.Components.ServiceRelation)
		}
		if c.Components.CascadeReason != "" {
			fmt.Fprintf(out, "       scaled by %.2f: %s\n", c.Components.Cascade, c.Components.CascadeReason)
		}
	}
}
