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

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Declare a new incident",
	Long:  `Creates a PagerDuty incident, opens a Slack bridge channel, and starts the MTTR timer.`,
	RunE:  runCreate,
}

func init() {
	createCmd.Flags().String("title", "", "Incident title (required)")
	createCmd.Flags().String("severity", "P2", "Severity level: P1, P2, P3, P4")
	createCmd.Flags().String("description", "", "Additional context for responders")
	_ = createCmd.MarkFlagRequired("title")
}

func runCreate(cmd *cobra.Command, args []string) error {
	title, _ := cmd.Flags().GetString("title")
	severity, _ := cmd.Flags().GetString("severity")
	description, _ := cmd.Flags().GetString("description")

	pdClient := pagerduty.NewClient(viper.GetString("PAGERDUTY_API_KEY"))
	slackClient := slack.NewClient(viper.GetString("SLACK_BOT_TOKEN"))
	t := timer.NewMTTRTimer()

	fmt.Printf("Declaring %s incident: %q\n", severity, title)

	incident, err := pdClient.CreateIncident(pagerduty.CreateIncidentInput{
		Title:       title,
		Severity:    severity,
		Description: description,
		ServiceID:   viper.GetString("PAGERDUTY_SERVICE_ID"),
	})
	if err != nil {
		return fmt.Errorf("creating PagerDuty incident: %w", err)
	}
	fmt.Printf("  PagerDuty incident created: %s\n", incident.ID)

	prefix := viper.GetString("SLACK_INCIDENT_CHANNEL_PREFIX")
	if prefix == "" {
		prefix = "inc"
	}
	channelName := fmt.Sprintf("%s-%s-%s",
		prefix,
		time.Now().Format("20060102"),
		incident.ID,
	)
	channel, err := slackClient.CreateChannel(channelName)
	if err != nil {
		return fmt.Errorf("creating Slack channel: %w", err)
	}
	fmt.Printf("  Slack channel created: #%s\n", channel.Name)

	_ = slackClient.PostMessage(channel.ID, fmt.Sprintf(
		":rotating_light: *%s INCIDENT DECLARED*\n*Title:* %s\n*PagerDuty:* %s\nMTTR timer started.",
		severity, title, incident.URL,
	))

	t.Start(incident.ID)
	fmt.Printf("  MTTR timer started at %s\n", t.StartedAt().Format(time.RFC3339))
	fmt.Printf("\nIncident ID: %s\n", incident.ID)
	fmt.Printf("Resolve with: incident resolve --id %s\n", incident.ID)

	return nil
}
