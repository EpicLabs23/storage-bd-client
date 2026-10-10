// Package cli is the storagebd terminal interface (cobra).
package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"storage-bd-client/internal/paths"
	"storage-bd-client/internal/store"
)

// Set at build time:
//
//	go build -ldflags "-X storage-bd-client/internal/cli.DefaultAPIURL=http://localhost:3000 -X storage-bd-client/internal/cli.Version=v0.1.0"
//
// DefaultAPIURL is the "storage.bd pre-configured" part: every new
// credential points here unless --api-url says otherwise.
var (
	DefaultAPIURL = "https://api.storage.bd"
	Version       = "dev"
)

type app struct {
	homeFlag string
	dataDir  string
	store    *store.Store
}

// openStore lazily opens the database, so --help and `version` work
// without touching the data directory.
func (a *app) openStore() (*store.Store, error) {
	if a.store != nil {
		return a.store, nil
	}
	dir, err := paths.DataDir(a.homeFlag)
	if err != nil {
		return nil, err
	}
	if err := paths.EnsureDataDir(dir); err != nil {
		return nil, err
	}
	s, err := store.Open(paths.DBPath(dir))
	if err != nil {
		return nil, err
	}
	a.store = s
	a.dataDir = dir
	return s, nil
}

func (a *app) close() {
	if a.store != nil {
		a.store.Close()
	}
}

func NewRootCmd() *cobra.Command {
	a := &app{}
	root := &cobra.Command{
		Use:           "storagebd",
		Short:         "storage.bd backup client",
		Long:          "storagebd backs up directories and databases on this machine to storage.bd.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPostRun: func(*cobra.Command, []string) {
			a.close()
		},
	}
	root.PersistentFlags().StringVar(&a.homeFlag, "home", "",
		fmt.Sprintf("data directory (default: $%s, /var/lib/storagebd as root, else ~/.local/share/storagebd)", paths.EnvHome))

	root.AddCommand(
		newCredentialCmd(a),
		newLoginCmd(a),
		newLogoutCmd(a),
		newAccountCmd(a),
		newConnectCmd(a),
		newActivateCmd(a),
		newRepoCmd(a),
		newSnapshotsCmd(a),
		newSettingsCmd(a),
		newSourceCmd(a),
		newBackupCmd(a),
		newRestoreCmd(a),
		newRestoreDBCmd(a),
		newJobsCmd(a),
		&cobra.Command{
			Use:   "version",
			Short: "Print the client version",
			Run: func(cmd *cobra.Command, _ []string) {
				fmt.Fprintf(cmd.OutOrStdout(), "storagebd %s (default API %s)\n", Version, DefaultAPIURL)
			},
		},
	)
	return root
}
