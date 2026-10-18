package cli

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"storage-bd-client/internal/account"
	"storage-bd-client/internal/server"
	"storage-bd-client/internal/store"
)

func newServeCmd(a *app) *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the local web UI and the backup scheduler",
		Long: `Run the storage.bd backup web UI and the backup scheduler.

Sign in to the page with a user created in the terminal:

  storagebd user add admin

By default the page listens on this machine only. On a remote server,
run this on your own computer (not the server) to open an SSH tunnel,
then open http://127.0.0.1:7878 in your computer's browser:

  ssh -i ~/.ssh/id_ed25519 -N -L 7878:127.0.0.1:7878 you@your-server

To open the page to the network instead (HTTPS and two-step sign-in
required; keep the port closed in the firewall when you're not using it):

  storagebd user 2fa enable admin
  storagebd settings set ui-tls-cert /etc/letsencrypt/live/backup.example.com/fullchain.pem
  storagebd settings set ui-tls-key  /etc/letsencrypt/live/backup.example.com/privkey.pem
  storagebd settings set ui-hosts backup.example.com
  storagebd settings set ui-listen 0.0.0.0:7878

Scheduled backups run only while this is running; install it as a systemd
service to keep them going.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			runner, err := a.jobRunner()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			st := runner.Store
			setting := func(key string) (string, error) {
				v, err := st.GetSetting(ctx, key)
				if errors.Is(err, store.ErrNotFound) {
					return "", nil
				}
				return v, err
			}
			if !cmd.Flags().Changed("listen") {
				if v, err := setting(store.SettingUIListen); err != nil {
					return err
				} else if v != "" {
					addr = v
				}
			}
			certFile, err := setting(store.SettingUITLSCert)
			if err != nil {
				return err
			}
			keyFile, err := setting(store.SettingUITLSKey)
			if err != nil {
				return err
			}
			hostsVal, err := setting(store.SettingUIHosts)
			if err != nil {
				return err
			}
			hosts, err := store.ParseUIHosts(hostsVal)
			if err != nil {
				return err
			}
			log.SetFlags(log.LstdFlags)
			s := &server.Server{
				Store:         st,
				Accounts:      &account.Manager{Store: st, UserAgent: "storagebd/" + Version},
				Repos:         runner.Repos,
				Runner:        runner,
				DataDir:       a.dataDir,
				DefaultAPIURL: DefaultAPIURL,
				Version:       Version,
				TLSCertFile:   certFile,
				TLSKeyFile:    keyFile,
				Hosts:         hosts,
			}
			users, err := st.ListUIUsers(ctx)
			if err != nil {
				return err
			}
			return s.Run(ctx, addr, func(rd server.Ready) {
				w := os.Stderr
				fmt.Fprintf(w, "storagebd UI is running at %s (stop with Ctrl-C)\n", rd.URL)
				for _, u := range rd.Also {
					fmt.Fprintf(w, "  also at %s\n", u)
				}
				if len(users) == 0 {
					fmt.Fprintln(w, "No users yet. Create one to sign in: storagebd user add admin")
				}
				if rd.Exposed {
					printExposedNotice(w, rd, users)
				}
			})
		},
	}
	cmd.Flags().StringVar(&addr, "listen", server.DefaultAddr, "address to listen on (default: the ui-listen setting, else 127.0.0.1:7878)")
	return cmd
}

// printExposedNotice is the firewall advice shown when the page is open
// to the network: the port should be open only while it's in use, and
// only to the admin's own address.
func printExposedNotice(w io.Writer, rd server.Ready, users []*store.UIUser) {
	port, via := rd.Port, ""
	if rd.Proxied {
		// storagebd itself is on loopback; what's exposed is the proxy.
		port, via = 443, " (through your reverse proxy;\nif it isn't on 443, use its port below)"
	}
	fmt.Fprintf(w, `
The backup page is OPEN TO THE NETWORK%[2]s. It can restore files anywhere
on this machine, so keep port %[1]d closed in the firewall except while you
use it, and open it only to your own IP address:

  ufw:        sudo ufw allow from YOUR.IP to any port %[1]d proto tcp
              sudo ufw delete allow from YOUR.IP to any port %[1]d proto tcp   (close)
  firewalld:  sudo firewall-cmd --add-rich-rule='rule family="ipv4" source address="YOUR.IP" port port="%[1]d" protocol="tcp" accept'
              (without --permanent, so it closes on the next reload or reboot)
  cloud:      add, then remove, an inbound rule for TCP %[1]d from YOUR.IP/32
              in the provider's security group / firewall.

Every sign-in needs two-step verification while the page is open.
`, port, via)
	var missing []string
	for _, u := range users {
		if !u.TOTPEnabled {
			missing = append(missing, u.Username)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(w, "These users can't sign in until they turn it on: %s\n  storagebd user 2fa enable <username>\n",
			strings.Join(missing, ", "))
	}
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
			for _, u := range st.Also {
				fmt.Fprintln(cmd.OutOrStdout(), u)
			}
			return nil
		},
	}
}
