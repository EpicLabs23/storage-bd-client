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

Sign in to the page with a user created in the terminal:

  storagebd user add admin

The page listens on this machine only. On a remote server, run this on
your own computer (not the server) to open an SSH tunnel, then open
http://127.0.0.1:7878 in your computer's browser:

  ssh -i ~/.ssh/id_ed25519 -N -L 7878:127.0.0.1:7878 you@your-server

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
			n, err := runner.Store.CountUIUsers(cmd.Context())
			if err != nil {
				return err
			}
			return s.Run(cmd.Context(), addr, func(url string) {
				fmt.Fprintf(os.Stderr, "storagebd UI is running at %s (stop with Ctrl-C)\n", url)
				if n == 0 {
					fmt.Fprintln(os.Stderr, "No users yet. Create one to sign in: storagebd user add admin")
				}
			})
		},
	}
	cmd.Flags().StringVar(&addr, "listen", server.DefaultAddr, "loopback address to listen on")
	return cmd
}

func newUIURLCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "ui-url",
		Short: "Print the address of the running web UI",
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
