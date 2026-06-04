package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/jamespham/incident-cli/internal/pagerduty"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List recent incidents",
	RunE:  runList,
}

func init() {
	listCmd.Flags().Duration("since", 24*time.Hour, "How far back to list incidents")
}

func runList(cmd *cobra.Command, args []string) error {
	since, _ := cmd.Flags().GetDuration("since")
	client := pagerduty.NewClient(viper.GetString("PAGERDUTY_API_KEY"))

	incidents, err := client.ListRecentIncidents(time.Now().Add(-since))
	if err != nil {
		return err
	}
	if len(incidents) == 0 {
		fmt.Println("No incidents in the selected window.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tCREATED\tSERVICE\tTITLE")
	for _, inc := range incidents {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			inc.ID, inc.CreatedAt.Format("2006-01-02 15:04"), inc.Service, inc.Title)
	}
	return w.Flush()
}
