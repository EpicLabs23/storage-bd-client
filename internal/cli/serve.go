package cli

import (
	"fmt"
	"log"
	"os"

	"github.com/spf13/cobra"

	"storage-bd-client/internal/account"
	"storage-bd-client/internal/server"
)

func newServeCmd(a *app) *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the local web UI and the backup scheduler",
		Long: `Run the storage.bd backup web UI and the backup scheduler.

The UI listens on loopback only and needs a private login link, which is
printed at startup and changes on every start. Get it again any time with:

  storagebd ui-url

On a remote server, open an SSH tunnel and use the link locally:

  ssh -L 7878:127.0.0.1:7878 your-server

Scheduled backups run only while this is running; install it as a systemd
service to keep them going.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := server.ValidateListenAddr(addr); err != nil {
				return err
			}
			runner, err := a.jobRunner()
			if err != nil {
				return err
			}
			log.SetFlags(log.LstdFlags)
			s := &server.Server{
				Store:         runner.Store,
				Accounts:      &account.Manager{Store: runner.Store, UserAgent: "storagebd/" + Version},
				Repos:         runner.Repos,
				Runner:        runner,
				DataDir:       a.dataDir,
				DefaultAPIURL: DefaultAPIURL,
				Version:       Version,
			}
			return s.Run(cmd.Context(), addr, func(url string) {
				fmt.Fprintf(os.Stderr, "storagebd UI is running. Open this private link in your browser:\n\n  %s\n\n"+
					"(Get it again with: storagebd ui-url. Stop with Ctrl-C.)\n", url)
			})
		},
	}
	cmd.Flags().StringVar(&addr, "listen", server.DefaultAddr, "loopback address to listen on")
	return cmd
}

func newUIURLCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "ui-url",
		Short: "Print the private login link of the running web UI",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := a.openStore(); err != nil {
				return err
			}
			st, err := server.ReadState(a.dataDir)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), st.URL)
			return nil
		},
	}
}
