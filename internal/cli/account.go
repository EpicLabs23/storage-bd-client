package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"storage-bd-client/internal/account"
	"storage-bd-client/internal/store"
)

func (a *app) accounts() (*account.Manager, error) {
	s, err := a.openStore()
	if err != nil {
		return nil, err
	}
	return &account.Manager{Store: s, UserAgent: "storagebd/" + Version}, nil
}

// apiURLFlag registers --api-url / --allow-http and returns a resolver
// that validates whatever was given (DefaultAPIURL if nothing was).
func apiURLFlag(cmd *cobra.Command) func() (string, error) {
	var raw string
	var allowHTTP bool
	cmd.Flags().StringVar(&raw, "api-url", DefaultAPIURL, "storage.bd API base URL")
	cmd.Flags().BoolVar(&allowHTTP, "allow-http", false, "permit a non-loopback plain-http API URL")
	return func() (string, error) { return store.ValidateAPIURL(raw, allowHTTP) }
}

func newLoginCmd(a *app) *cobra.Command {
	var email, passwordFile string
	var noRemember bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in to your storage.bd account",
		Long: `Log in to your storage.bd account so this machine can create and activate
backup repos for you (storagebd connect).

The login is kept for 30 days of inactivity (24 hours with --no-remember).
Backups never need it: they use the per-repo credential instead.

No account yet? Sign up at https://storage.bd and verify your email first.`,
		Args: cobra.NoArgs,
	}
	resolve := apiURLFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		apiURL, err := resolve()
		if err != nil {
			return err
		}
		if email == "" {
			if email, err = promptLine("Email: "); err != nil {
				return err
			}
		}
		password, err := readSecret(passwordFile, "Password: ")
		if err != nil {
			return err
		}
		m, err := a.accounts()
		if err != nil {
			return err
		}
		if err := m.Login(cmd.Context(), apiURL, email, password, !noRemember); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Logged in to %s as %s\n", apiURL, strings.ToLower(strings.TrimSpace(email)))
		return nil
	}
	cmd.Flags().StringVar(&email, "email", "", "account email (prompted if omitted)")
	cmd.Flags().StringVar(&passwordFile, "password-file", "", `read the password from a file ("-" = stdin)`)
	cmd.Flags().BoolVar(&noRemember, "no-remember", false, "expire the login after 24 hours instead of 30 days")
	return cmd
}

func newLogoutCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Log out of your storage.bd account (backups keep working)",
		Args:  cobra.NoArgs,
	}
	resolve := apiURLFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		apiURL, err := resolve()
		if err != nil {
			return err
		}
		m, err := a.accounts()
		if err != nil {
			return err
		}
		if err := m.Logout(cmd.Context(), apiURL); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Logged out. Stored backup credentials were not changed.")
		return nil
	}
	return cmd
}

func newAccountCmd(a *app) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "account",
		Short: "Show your storage.bd account: balance and backup repos",
		Args:  cobra.NoArgs,
	}
	resolve := apiURLFlag(cmd)
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		apiURL, err := resolve()
		if err != nil {
			return err
		}
		m, err := a.accounts()
		if err != nil {
			return err
		}
		o, err := m.Overview(cmd.Context(), apiURL)
		if err != nil {
			return err
		}
		local := map[string]string{}
		creds, err := m.Store.ListCredentials(cmd.Context())
		if err != nil {
			return err
		}
		for _, c := range creds {
			if c.APIURL == apiURL {
				local[c.TenantID] = c.Name
			}
		}

		out := cmd.OutOrStdout()
		if asJSON {
			type tenantView struct {
				TenantID      string    `json:"tenant_id"`
				Label         string    `json:"label"`
				StorageGB     int       `json:"storage_gb"`
				State         string    `json:"state"`
				NextBillingAt time.Time `json:"next_billing_at"`
				LocalName     string    `json:"local_credential,omitempty"`
			}
			v := struct {
				APIURL   string       `json:"api_url"`
				Email    string       `json:"email"`
				Balance  string       `json:"balance"`
				Currency string       `json:"currency"`
				Tenants  []tenantView `json:"tenants"`
			}{APIURL: o.APIURL, Email: o.Email, Balance: o.Balance, Currency: o.Currency, Tenants: []tenantView{}}
			for _, t := range o.Tenants {
				v.Tenants = append(v.Tenants, tenantView{t.TenantID, t.LocalLabel, t.StorageGB, t.State, t.NextBillingAt, local[t.TenantID]})
			}
			return writeJSON(out, v)
		}

		fmt.Fprintf(out, "Account:  %s\nAPI:      %s\nBalance:  %s %s\n\n", o.Email, o.APIURL, o.Balance, o.Currency)
		if len(o.Tenants) == 0 {
			fmt.Fprintln(out, "No backup spaces yet. Create one for this machine with: storagebd connect")
			return nil
		}
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tSIZE\tSTATE\tRENEWS\tON THIS MACHINE\tSPACE ID")
		for _, t := range o.Tenants {
			state := spaceStateLabel(t.State)
			if t.SuspendReason != nil && *t.SuspendReason != "" {
				reason := *t.SuspendReason
				if reason == "balance_exhausted" {
					reason = "balance ran out"
				}
				state += " (" + reason + ")"
			}
			renews := "-"
			if t.State == "active" || t.State == "grace" {
				renews = t.NextBillingAt.Local().Format(time.DateOnly)
			}
			onMachine := local[t.TenantID]
			if onMachine == "" {
				onMachine = "-"
			}
			fmt.Fprintf(tw, "%s\t%d GB\t%s\t%s\t%s\t%s\n", t.LocalLabel, t.StorageGB, state, renews, onMachine, t.TenantID)
		}
		return tw.Flush()
	}
	return cmd
}

func newConnectCmd(a *app) *cobra.Command {
	var name, label string
	var storageGB int
	var noActivate, yes bool
	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Create a backup space for this machine on your storage.bd account",
		Long: `Create a new storage.bd backup space for this machine and save its access
key here, with a newly generated encryption (restic) password.

Activation charges the first month (storage size x the current per-GB rate)
from your account balance. If the balance is too low the space is still created
and saved, and you can top up and run "storagebd activate" later.

Requires: storagebd login`,
		Args: cobra.NoArgs,
	}
	resolve := apiURLFlag(cmd)
	f := cmd.Flags()
	f.StringVar(&name, "name", account.DefaultName(), "local credential name")
	f.StringVar(&label, "label", account.DefaultLabel(), "space name shown in the storage.bd dashboard")
	f.IntVar(&storageGB, "storage-gb", 0, "storage to pay for, in GB (required)")
	f.BoolVar(&noActivate, "no-activate", false, "create the space but don't activate (charge) it yet")
	f.BoolVarP(&yes, "yes", "y", false, "don't ask to confirm the charge")
	cmd.MarkFlagRequired("storage-gb")

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		apiURL, err := resolve()
		if err != nil {
			return err
		}
		m, err := a.accounts()
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		out := cmd.OutOrStdout()

		if !noActivate && !yes {
			q, err := m.Quote(ctx, apiURL, storageGB)
			if err != nil {
				return err
			}
			ok, err := confirm(fmt.Sprintf("Create and activate a %d GB backup space %q? This charges %s %s now (%s/GB/month), then monthly.",
				storageGB, label, q.Monthly, q.Currency, q.PricePerGB))
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("aborted")
			}
		}

		res, err := m.Connect(ctx, account.ConnectParams{
			APIURL: apiURL, Name: name, Label: label, StorageGB: storageGB, Activate: !noActivate,
		})
		var orphan *account.OrphanError
		if errors.As(err, &orphan) {
			// The space secret key can't be fetched again; this is the only
			// chance to keep it. stderr, so it isn't piped onward by accident.
			fmt.Fprintf(os.Stderr, "\nSave these now; the space secret key cannot be shown again:\n"+
				"  Space ID:       %s\n  Space access key ID:  %s\n  Space secret key:     %s\n"+
				"Then add it with: storagebd credential add <name> --space-id %s --key-id %s\n\n",
				orphan.TenantID, orphan.InstallID, orphan.InstallSecret, orphan.TenantID, orphan.InstallID)
			return err
		}
		if err != nil {
			return err
		}

		fmt.Fprintf(out, "Created backup space %q (space ID %s) and saved it as credential %q", label, res.Credential.TenantID, res.Credential.Name)
		if res.Credential.IsDefault {
			fmt.Fprint(out, " (default)")
		}
		fmt.Fprintln(out)
		switch {
		case res.Activated:
			fmt.Fprintln(out, "Activated. This machine is ready to back up.")
		case res.ActivationError != nil:
			fmt.Fprintf(out, "Not activated yet: %v\n", res.ActivationError)
		default:
			fmt.Fprintf(out, "Not activated. When ready: storagebd activate %s\n", res.Credential.Name)
		}
		return nil
	}
	return cmd
}

func newActivateCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "activate [credential]",
		Short: "Activate (pay for) a backup space created with --no-activate or after topping up",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := a.accounts()
			if err != nil {
				return err
			}
			cred, err := credentialArg(cmd, m.Store, args)
			if err != nil {
				return err
			}
			if !yes {
				ok, err := confirm(fmt.Sprintf("Activate %q? This charges the first month from your storage.bd balance.", cred.Name))
				if err != nil {
					return err
				}
				if !ok {
					return errors.New("aborted")
				}
			}
			if err := m.Activate(cmd.Context(), cred); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Activated %q.\n", cred.Name)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask to confirm the charge")
	return cmd
}

func newCredentialTestCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "test [name]",
		Short: "Check that storage.bd accepts a credential",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := a.accounts()
			if err != nil {
				return err
			}
			cred, err := credentialArg(cmd, m.Store, args)
			if err != nil {
				return err
			}
			if err := m.TestCredential(cmd.Context(), cred); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "OK: storage.bd accepted credential %q (space ID %s)\n", cred.Name, cred.TenantID)

			return nil
		},
	}
}

// credentialArg resolves an optional [name] argument to a credential,
// falling back to the default.
func credentialArg(cmd *cobra.Command, s *store.Store, args []string) (*store.Credential, error) {
	if len(args) == 1 {
		c, err := s.GetCredentialByName(cmd.Context(), args[0])
		if err != nil {
			return nil, notFound(args[0], err)
		}
		return c, nil
	}
	c, err := s.GetDefaultCredential(cmd.Context())
	if errors.Is(err, store.ErrNotFound) {
		return nil, errors.New("no default credential; pass a name, or run: storagebd connect")
	}
	return c, err
}

// spaceStateLabel matches the state names the storage.bd dashboard shows.
// --json output keeps the raw API values.
func spaceStateLabel(s string) string {
	switch s {
	case "active":
		return "Active"
	case "grace":
		return "Over limit"
	case "suspended":
		return "Suspended"
	case "cancelled":
		return "Cancelled"
	case "purged":
		return "Deleted"
	}
	return s
}
