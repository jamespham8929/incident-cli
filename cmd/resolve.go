package cmd

import (
	"fmt"
	"time"

	"github.com/jamespham/incident-cli/internal/pagerduty"
	"github.com/jamespham/incident-cli/internal/slack"
	"github.com/jamespham/incident-cli/internal/timer"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var resolveCmd = &cobra.Command{
	Use:   "resolve",
	Short: "Resolve an active incident and record MTTR",
	RunE:  runResolve,
}

func init() {
	resolveCmd.Flags().String("id", "", "Incident ID to resolve (required)")
	resolveCmd.Flags().String("resolution", "", "Brief description of how the incident was resolved")
	_ = resolveCmd.MarkFlagRequired("id")
}

func runResolve(cmd *cobra.Command, args []string) error {
	id, _ := cmd.Flags().GetString("id")
	resolution, _ := cmd.Flags().GetString("resolution")

	pdClient := pagerduty.NewClient(viper.GetString("PAGERDUTY_API_KEY"))
	slackClient := slack.NewClient(viper.GetString("SLACK_BOT_TOKEN"))
	t := timer.NewMTTRTimer()

	t.Load(id)

	if err := pdClient.ResolveIncident(id, resolution); err != nil {
		return fmt.Errorf("resolving PagerDuty incident: %w", err)
	}

	mttr := t.Stop(id)
	fmt.Printf("Incident %s resolved.\n", id)
	fmt.Printf("MTTR: %s\n", formatDuration(mttr))

	channelName := fmt.Sprintf("inc-%s-%s", time.Now().Format("20060102"), id)
	if ch, err := slackClient.FindChannel(channelName); err == nil {
		_ = slackClient.PostMessage(ch.ID, fmt.Sprintf(
			":white_check_mark: *INCIDENT RESOLVED* — %s\nMTTR: *%s*\nResolution: %s\n\nRun `incident postmortem --id %s` to generate the post-mortem.",
			id, formatDuration(mttr), resolution, id,
		))
		_ = slackClient.ArchiveChannel(ch.ID)
	}

	return nil
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
}
