package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"storage-bd-client/internal/store"
)

func newCredentialCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "credential",
		Aliases: []string{"cred", "credentials"},
		Short:   "Manage storage.bd connect credentials stored on this machine",
		Long: `A credential links this machine to one storage.bd backup repo (tenant):
the install ID/secret used to authenticate to storage.bd, and the restic
password that encrypts the repo. This machine holds the primary copy of that
password; storage.bd keeps only a support-gated escrow copy.`,
	}
	cmd.AddCommand(
		newCredentialListCmd(a),
		newCredentialAddCmd(a),
		newCredentialShowCmd(a),
		newCredentialUpdateCmd(a),
		newCredentialUseCmd(a),
		newCredentialRemoveCmd(a),
	)
	return cmd
}

// credentialView is the printable/JSON shape. Secrets are only filled
// in when the caller explicitly asked for --reveal.
type credentialView struct {
	Name               string     `json:"name"`
	Default            bool       `json:"default"`
	APIURL             string     `json:"api_url"`
	TenantID           string     `json:"tenant_id"`
	InstallID          string     `json:"install_id"`
	AccountEmail       string     `json:"account_email,omitempty"`
	InstallSecret      string     `json:"install_secret,omitempty"`
	ResticPassword     string     `json:"restic_password,omitempty"`
	RepoInitializedAt  *time.Time `json:"repo_initialized_at"`
	PasswordEscrowedAt *time.Time `json:"password_escrowed_at"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

func toView(c *store.Credential, reveal bool) credentialView {
	v := credentialView{
		Name: c.Name, Default: c.IsDefault, APIURL: c.APIURL, TenantID: c.TenantID,
		InstallID: c.InstallID, AccountEmail: c.AccountEmail,
		RepoInitializedAt: c.RepoInitializedAt, PasswordEscrowedAt: c.PasswordEscrowedAt,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
	if reveal {
		v.InstallSecret = c.InstallSecret
		v.ResticPassword = c.ResticPassword
	}
	return v
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func notFound(name string, err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("no credential named %q (see: storagebd credential list)", name)
	}
	return err
}

func newCredentialListCmd(a *app) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List credentials (secrets are never shown here)",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.openStore()
			if err != nil {
				return err
			}
			creds, err := s.ListCredentials(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				views := make([]credentialView, 0, len(creds))
				for _, c := range creds {
					views = append(views, toView(c, false))
				}
				return writeJSON(out, views)
			}
			if len(creds) == 0 {
				fmt.Fprintln(out, "No credentials yet. Add one with: storagebd credential add <name> ...")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "\tNAME\tTENANT\tAPI\tESCROWED")
			for _, c := range creds {
				mark := ""
				if c.IsDefault {
					mark = "*"
				}
				escrowed := "no"
				if c.PasswordEscrowedAt != nil {
					escrowed = "yes"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", mark, c.Name, c.TenantID, c.APIURL, escrowed)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return cmd
}

func newCredentialAddCmd(a *app) *cobra.Command {
	var (
		tenantID, installID, apiURL, email   string
		installSecretFile, resticPassFile    string
		allowHTTP, makeDefault, existingRepo bool
	)
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Store a connect credential from the storage.bd dashboard",
		Long: `Store a connect credential for one storage.bd tenant.

The install secret is prompted for (no echo), or read from --install-secret-file
(use "-" for stdin). It is never accepted as a plain argument, since arguments
are visible to other users in the process list.

For a new, empty repo a strong restic password is generated for you. If the
repo was already initialized elsewhere, pass --existing-repo to be prompted for
its password instead (or --restic-password-file).`,
		Example: `  storagebd credential add laptop --tenant-id 3f1c... --install-id 9a2e...
  storagebd credential add web1 --tenant-id ... --install-id ... --install-secret-file - < secret.txt`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := store.ValidateName(name); err != nil {
				return err
			}
			if err := store.ValidateUUID("--tenant-id", tenantID); err != nil {
				return err
			}
			if err := store.ValidateUUID("--install-id", installID); err != nil {
				return err
			}
			normURL, err := store.ValidateAPIURL(apiURL, allowHTTP)
			if err != nil {
				return err
			}
			if installSecretFile == "-" && resticPassFile == "-" {
				return errors.New("only one of --install-secret-file / --restic-password-file can read stdin")
			}

			secret, err := readSecret(installSecretFile, "Install secret: ")
			if err != nil {
				return err
			}
			if err := store.ValidateInstallSecret(secret); err != nil {
				return err
			}

			var password string
			generated := false
			switch {
			case resticPassFile != "":
				password, err = readSecretFile(resticPassFile)
			case existingRepo:
				password, err = readSecret("", "Existing repo's restic password: ")
			default:
				password, err = store.GenerateResticPassword()
				generated = true
			}
			if err != nil {
				return err
			}

			s, err := a.openStore()
			if err != nil {
				return err
			}
			c := &store.Credential{
				Name: name, APIURL: normURL, TenantID: tenantID, InstallID: installID,
				InstallSecret: secret, ResticPassword: password, AccountEmail: email,
				IsDefault: makeDefault,
			}
			if err := s.CreateCredential(cmd.Context(), c); err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Saved credential %q", name)
			if c.IsDefault {
				fmt.Fprint(out, " (default)")
			}
			fmt.Fprintln(out)
			if generated {
				fmt.Fprintln(out, "Generated a new restic password for this repo. It is escrowed with storage.bd on first backup;")
				fmt.Fprintf(out, "until then this machine holds the only copy (view it with: storagebd credential show %s --reveal).\n", name)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&tenantID, "tenant-id", "", "tenant ID (required)")
	f.StringVar(&installID, "install-id", "", "install ID (required)")
	f.StringVar(&apiURL, "api-url", DefaultAPIURL, "storage.bd API base URL")
	f.BoolVar(&allowHTTP, "allow-http", false, "permit a non-loopback plain-http API URL")
	f.StringVar(&email, "account-email", "", "storage.bd account email (informational)")
	f.StringVar(&installSecretFile, "install-secret-file", "", `read the install secret from a file ("-" = stdin)`)
	f.StringVar(&resticPassFile, "restic-password-file", "", `read an existing repo's restic password from a file ("-" = stdin)`)
	f.BoolVar(&existingRepo, "existing-repo", false, "repo is already initialized: prompt for its restic password")
	f.BoolVar(&makeDefault, "default", false, "make this the default credential")
	cmd.MarkFlagRequired("tenant-id")
	cmd.MarkFlagRequired("install-id")
	cmd.MarkFlagsMutuallyExclusive("existing-repo", "restic-password-file")
	return cmd
}

func newCredentialShowCmd(a *app) *cobra.Command {
	var reveal, asJSON bool
	cmd := &cobra.Command{
		Use:   "show [name]",
		Short: "Show one credential (the default if no name is given)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.openStore()
			if err != nil {
				return err
			}
			var c *store.Credential
			if len(args) == 1 {
				c, err = s.GetCredentialByName(cmd.Context(), args[0])
				if err != nil {
					return notFound(args[0], err)
				}
			} else {
				c, err = s.GetDefaultCredential(cmd.Context())
				if errors.Is(err, store.ErrNotFound) {
					return errors.New("no default credential; pass a name or add one first")
				} else if err != nil {
					return err
				}
			}

			v := toView(c, reveal)
			out := cmd.OutOrStdout()
			if asJSON {
				return writeJSON(out, v)
			}
			secretOrMask := func(s string) string {
				if reveal {
					return s
				}
				return "******** (use --reveal)"
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintf(tw, "Name:\t%s\n", v.Name)
			fmt.Fprintf(tw, "Default:\t%t\n", v.Default)
			fmt.Fprintf(tw, "API URL:\t%s\n", v.APIURL)
			fmt.Fprintf(tw, "Tenant ID:\t%s\n", v.TenantID)
			fmt.Fprintf(tw, "Install ID:\t%s\n", v.InstallID)
			if v.AccountEmail != "" {
				fmt.Fprintf(tw, "Account:\t%s\n", v.AccountEmail)
			}
			fmt.Fprintf(tw, "Install secret:\t%s\n", secretOrMask(c.InstallSecret))
			fmt.Fprintf(tw, "Restic password:\t%s\n", secretOrMask(c.ResticPassword))
			fmt.Fprintf(tw, "Repo initialized:\t%s\n", fmtTime(v.RepoInitializedAt))
			fmt.Fprintf(tw, "Password escrowed:\t%s\n", fmtTime(v.PasswordEscrowedAt))
			fmt.Fprintf(tw, "Created:\t%s\n", v.CreatedAt.Local().Format(time.DateTime))
			fmt.Fprintf(tw, "Updated:\t%s\n", v.UpdatedAt.Local().Format(time.DateTime))
			if err := tw.Flush(); err != nil {
				return err
			}
			if reveal && !asJSON {
				fmt.Fprintln(os.Stderr, "Warning: secrets printed above. Clear your terminal scrollback if this screen is shared or logged.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&reveal, "reveal", false, "include the install secret and restic password")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return cmd
}

func fmtTime(t *time.Time) string {
	if t == nil {
		return "no"
	}
	return t.Local().Format(time.DateTime)
}

func newCredentialUpdateCmd(a *app) *cobra.Command {
	var (
		rename, apiURL, email                     string
		installSecretFile, resticPassFile         string
		allowHTTP, makeDefault                    bool
		promptInstallSecret, promptResticPassword bool
	)
	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "Change a stored credential",
		Long: `Change fields of a stored credential. Only the flags you pass are changed.

--install-secret replaces the install secret after it was regenerated in the
storage.bd dashboard. --restic-password only corrects the locally stored copy;
it does NOT change the repo's actual password. Storing a wrong value here will
make every backup and restore fail.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.openStore()
			if err != nil {
				return err
			}
			c, err := s.GetCredentialByName(cmd.Context(), args[0])
			if err != nil {
				return notFound(args[0], err)
			}

			if installSecretFile == "-" && resticPassFile == "-" {
				return errors.New("only one of --install-secret-file / --restic-password-file can read stdin")
			}
			changed := false
			f := cmd.Flags()
			if f.Changed("rename") {
				c.Name, changed = rename, true
			}
			if f.Changed("api-url") {
				if c.APIURL, err = store.ValidateAPIURL(apiURL, allowHTTP); err != nil {
					return err
				}
				changed = true
			}
			if f.Changed("account-email") {
				c.AccountEmail, changed = email, true
			}
			if makeDefault {
				c.IsDefault, changed = true, true
			}
			if promptInstallSecret || installSecretFile != "" {
				if c.InstallSecret, err = readSecret(installSecretFile, "New install secret: "); err != nil {
					return err
				}
				changed = true
			}
			if promptResticPassword || resticPassFile != "" {
				if c.ResticPassword, err = readSecret(resticPassFile, "Correct restic password: "); err != nil {
					return err
				}
				changed = true
			}
			if !changed {
				return errors.New("nothing to update; pass at least one flag (see --help)")
			}
			if err := s.UpdateCredential(cmd.Context(), c); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Updated credential %q\n", c.Name)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&rename, "rename", "", "new name")
	f.StringVar(&apiURL, "api-url", "", "new storage.bd API base URL")
	f.BoolVar(&allowHTTP, "allow-http", false, "permit a non-loopback plain-http API URL")
	f.StringVar(&email, "account-email", "", "storage.bd account email (informational)")
	f.BoolVar(&makeDefault, "default", false, "make this the default credential")
	f.BoolVar(&promptInstallSecret, "install-secret", false, "prompt for a new install secret")
	f.StringVar(&installSecretFile, "install-secret-file", "", `read a new install secret from a file ("-" = stdin)`)
	f.BoolVar(&promptResticPassword, "restic-password", false, "prompt for the correct restic password (local copy only)")
	f.StringVar(&resticPassFile, "restic-password-file", "", `read the correct restic password from a file ("-" = stdin)`)
	cmd.MarkFlagsMutuallyExclusive("install-secret", "install-secret-file")
	cmd.MarkFlagsMutuallyExclusive("restic-password", "restic-password-file")
	return cmd
}

func newCredentialUseCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Make a credential the default",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.openStore()
			if err != nil {
				return err
			}
			if err := s.SetDefaultCredential(cmd.Context(), args[0]); err != nil {
				return notFound(args[0], err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%q is now the default credential\n", args[0])
			return nil
		},
	}
}

func newCredentialRemoveCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm", "delete"},
		Short:   "Delete a credential from this machine",
		Long: `Delete a credential from this machine. The repo and its snapshots on
storage.bd are not touched.

If the restic password was never escrowed and you have no other copy, the
repo's data cannot be decrypted after this.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.openStore()
			if err != nil {
				return err
			}
			c, err := s.GetCredentialByName(cmd.Context(), args[0])
			if err != nil {
				return notFound(args[0], err)
			}
			if !yes {
				q := fmt.Sprintf("Delete credential %q?", c.Name)
				if c.PasswordEscrowedAt == nil {
					q = fmt.Sprintf("Credential %q's restic password was never escrowed with storage.bd. "+
						"Without another copy, its backups become unreadable. Delete anyway?", c.Name)
				}
				ok, err := confirm(q)
				if err != nil {
					return err
				}
				if !ok {
					return errors.New("aborted")
				}
			}
			if err := s.DeleteCredential(cmd.Context(), c.Name); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted credential %q\n", c.Name)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	return cmd
}
