package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

// newRootCmd builds the root command and wires up every subcommand. Each
// command is constructed fresh so that flag state never leaks between
// invocations, which keeps the commands testable in isolation.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "incident",
		Short: "Incident management CLI for on-call engineers",
		Long: `incident-cli automates incident declaration, Slack channel creation,
MTTR tracking, and post-mortem generation.`,
	}
	root.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: $HOME/.incident.yaml)")
	root.AddCommand(newCreateCmd())
	root.AddCommand(newResolveCmd())
	root.AddCommand(newListCmd())
	root.AddCommand(newInvestigateCmd())
	root.AddCommand(newPostmortemCmd())
	return root
}

func Execute() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		home, err := os.UserHomeDir()
		if err == nil {
			viper.AddConfigPath(home)
		}
		viper.SetConfigType("yaml")
		viper.SetConfigName(".incident")
	}
	viper.AutomaticEnv()
	_ = viper.ReadInConfig()
}
