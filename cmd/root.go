package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "incident",
	Short: "Incident management CLI for on-call engineers",
	Long: `incident-cli automates incident declaration, Slack channel creation,
MTTR tracking, and post-mortem generation.`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: $HOME/.incident.yaml)")
	rootCmd.AddCommand(createCmd)
	rootCmd.AddCommand(resolveCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(postmortemCmd)
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
