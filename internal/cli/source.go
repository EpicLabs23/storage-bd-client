package cli

import (
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"storage-bd-client/internal/store"
)

// credFlag registers --cred and resolves it (default credential if unset).
func credFlag(cmd *cobra.Command) func(s *store.Store) (*store.Credential, error) {
	var name string
	cmd.Flags().StringVar(&name, "cred", "", "credential (repo) to use (default: the default credential)")
	return func(s *store.Store) (*store.Credential, error) {
		var args []string
		if name != "" {
			args = []string{name}
		}
		return credentialArg(cmd, s, args)
	}
}

func newSourceCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "source",
		Aliases: []string{"sources"},
		Short:   "Manage what gets backed up: directories and databases",
		Long: `Sources are what a backup run saves into a credential's repo. Each source
gets its own snapshot per run, tagged source:<name>.`,
	}
	cmd.AddCommand(newSourceListCmd(a), newSourceAddDirCmd(a), newSourceAddDBCmd(a),
		newSourceUpdateCmd(a), newSourceRemoveCmd(a))
	return cmd
}

func newSourceListCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "List sources", Args: cobra.NoArgs}
	resolve := credFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		s, err := a.openStore()
		if err != nil {
			return err
		}
		cred, err := resolve(s)
		if err != nil {
			return err
		}
		srcs, err := s.ListSources(cmd.Context(), cred.ID)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if len(srcs) == 0 {
			fmt.Fprintf(out, "No sources for %q yet. Add one with: storagebd source add-dir / add-db\n", cred.Name)
			return nil
		}
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tKIND\tENABLED\tWHAT")
		for _, src := range srcs {
			fmt.Fprintf(tw, "%s\t%s\t%t\t%s\n", src.Name, src.Kind, src.Enabled, describeSource(src))
		}
		return tw.Flush()
	}
	return cmd
}

func describeSource(s *store.Source) string {
	if s.Kind == store.KindDir {
		d := s.Path
		if len(s.Excludes) > 0 {
			d += "  (excludes: " + strings.Join(s.Excludes, ", ") + ")"
		}
		return d
	}
	where := s.DBHost
	if where == "" {
		where = "local socket"
		if s.DBSocket != "" {
			where = s.DBSocket
		}
	}
	if s.DBPort != 0 {
		where += fmt.Sprintf(":%d", s.DBPort)
	}
	user := s.DBUser
	if user == "" {
		user = "(OS user)"
	}
	return fmt.Sprintf("%s as %s @ %s", s.DBName, user, where)
}

func newSourceAddDirCmd(a *app) *cobra.Command {
	var excludes []string
	cmd := &cobra.Command{
		Use:     "add-dir <name> <path>",
		Short:   "Back up a directory",
		Example: "  storagebd source add-dir www /var/www --exclude '*.log' --exclude /var/www/cache",
		Args:    cobra.ExactArgs(2),
	}
	resolve := credFlag(cmd)
	cmd.Flags().StringArrayVar(&excludes, "exclude", nil, "restic exclude pattern (repeatable)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		s, err := a.openStore()
		if err != nil {
			return err
		}
		cred, err := resolve(s)
		if err != nil {
			return err
		}
		src := &store.Source{CredentialID: cred.ID, Name: args[0], Kind: store.KindDir, Path: args[1],
			Excludes: excludes, Enabled: true}
		if err := checkDirExists(src.Path); err != nil {
			return err
		}
		if err := s.CreateSource(cmd.Context(), src); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Added directory source %q (%s) to %q\n", src.Name, src.Path, cred.Name)
		return nil
	}
	return cmd
}

type dbFlags struct {
	engine, database, host, socket, user, passwordFile string
	port                                               int
	noPassword                                         bool
}

func (d *dbFlags) register(cmd *cobra.Command, withEngine bool) {
	f := cmd.Flags()
	if withEngine {
		f.StringVar(&d.engine, "engine", "", "mysql (also MariaDB) or postgres (required)")
	}
	f.StringVar(&d.database, "database", "", "database name")
	f.StringVar(&d.host, "host", "", "server host (default: local socket)")
	f.IntVar(&d.port, "port", 0, "server port (default: the engine's)")
	f.StringVar(&d.socket, "socket", "", "unix socket path")
	f.StringVar(&d.user, "user", "", "database user")
	f.StringVar(&d.passwordFile, "password-file", "", `read the database password from a file ("-" = stdin)`)
	f.BoolVar(&d.noPassword, "no-password", false, "no password (e.g. Postgres peer auth)")
}

func newSourceAddDBCmd(a *app) *cobra.Command {
	var d dbFlags
	cmd := &cobra.Command{
		Use:   "add-db <name>",
		Short: "Back up a MySQL/MariaDB or Postgres database",
		Long: `Back up a database by dumping it with the engine's own tool (mariadb-dump /
mysqldump --single-transaction, or pg_dump) straight into restic.

The password is prompted for (no echo) or read from --password-file. It is
stored with this machine's other secrets and reaches the dump tool via a
0600 option file, never the command line.`,
		Example: `  storagebd source add-db shop --engine mysql --database shop --user shop_backup
  storagebd source add-db app --engine postgres --database app --user app --host 127.0.0.1`,
		Args: cobra.ExactArgs(1),
	}
	resolve := credFlag(cmd)
	d.register(cmd, true)
	cmd.MarkFlagRequired("engine")
	cmd.MarkFlagRequired("database")
	cmd.MarkFlagsMutuallyExclusive("password-file", "no-password")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		kind := d.engine
		if kind == "mariadb" {
			kind = store.KindMySQL
		}
		if kind == "postgresql" || kind == "pg" {
			kind = store.KindPostgres
		}
		src := &store.Source{Name: args[0], Kind: kind, DBName: d.database, DBHost: d.host, DBPort: d.port,
			DBSocket: d.socket, DBUser: d.user, Enabled: true}
		if err := src.Validate(); err != nil {
			return err
		}
		if !d.noPassword {
			pw, err := readSecret(d.passwordFile, "Database password: ")
			if err != nil {
				return err
			}
			src.DBPassword = pw
		}
		s, err := a.openStore()
		if err != nil {
			return err
		}
		cred, err := resolve(s)
		if err != nil {
			return err
		}
		src.CredentialID = cred.ID
		if err := s.CreateSource(cmd.Context(), src); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Added %s source %q (%s) to %q\n", src.Kind, src.Name, describeSource(src), cred.Name)
		return nil
	}
	return cmd
}

func newSourceUpdateCmd(a *app) *cobra.Command {
	var d dbFlags
	var rename, path string
	var excludes []string
	var clearExcludes, enable, disable, promptPassword bool
	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "Change a source (only the flags you pass are changed)",
		Args:  cobra.ExactArgs(1),
	}
	resolve := credFlag(cmd)
	d.register(cmd, false)
	f := cmd.Flags()
	f.StringVar(&rename, "rename", "", "new name (existing snapshots keep the old source tag)")
	f.StringVar(&path, "path", "", "new directory path")
	f.StringArrayVar(&excludes, "exclude", nil, "replace the exclude patterns (repeatable)")
	f.BoolVar(&clearExcludes, "clear-excludes", false, "remove all exclude patterns")
	f.BoolVar(&enable, "enable", false, "include in default backup runs")
	f.BoolVar(&disable, "disable", false, "skip in default backup runs")
	f.BoolVar(&promptPassword, "password", false, "prompt for a new database password")
	cmd.MarkFlagsMutuallyExclusive("enable", "disable")
	cmd.MarkFlagsMutuallyExclusive("exclude", "clear-excludes")
	cmd.MarkFlagsMutuallyExclusive("password", "password-file", "no-password")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		s, err := a.openStore()
		if err != nil {
			return err
		}
		cred, err := resolve(s)
		if err != nil {
			return err
		}
		src, err := s.GetSource(cmd.Context(), cred.ID, args[0])
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no source named %q for %q", args[0], cred.Name)
		} else if err != nil {
			return err
		}

		changed := cmd.Flags().Changed
		isDB := src.IsDB()
		dirOnly := []string{"path", "exclude", "clear-excludes"}
		dbOnly := []string{"database", "host", "port", "socket", "user", "password", "password-file", "no-password"}
		for _, fl := range dirOnly {
			if changed(fl) && isDB {
				return fmt.Errorf("--%s applies to directory sources only", fl)
			}
		}
		for _, fl := range dbOnly {
			if changed(fl) && !isDB {
				return fmt.Errorf("--%s applies to database sources only", fl)
			}
		}

		any := false
		set := func(flag string, apply func()) {
			if changed(flag) {
				apply()
				any = true
			}
		}
		set("rename", func() { src.Name = rename })
		set("path", func() { src.Path = path })
		set("exclude", func() { src.Excludes = excludes })
		set("clear-excludes", func() { src.Excludes = nil })
		set("enable", func() { src.Enabled = true })
		set("disable", func() { src.Enabled = false })
		set("database", func() { src.DBName = d.database })
		set("host", func() { src.DBHost = d.host })
		set("port", func() { src.DBPort = d.port })
		set("socket", func() { src.DBSocket = d.socket })
		set("user", func() { src.DBUser = d.user })
		set("no-password", func() { src.DBPassword = "" })
		if promptPassword || d.passwordFile != "" {
			pw, err := readSecret(d.passwordFile, "New database password: ")
			if err != nil {
				return err
			}
			src.DBPassword, any = pw, true
		}
		if !any {
			return errors.New("nothing to update; pass at least one flag (see --help)")
		}
		if changed("path") {
			if err := checkDirExists(src.Path); err != nil {
				return err
			}
		}
		if err := s.UpdateSource(cmd.Context(), src); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Updated source %q\n", src.Name)
		return nil
	}
	return cmd
}

func newSourceRemoveCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm", "delete"},
		Short:   "Stop backing up a source (existing snapshots are kept)",
		Args:    cobra.ExactArgs(1),
	}
	resolve := credFlag(cmd)
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		s, err := a.openStore()
		if err != nil {
			return err
		}
		cred, err := resolve(s)
		if err != nil {
			return err
		}
		if !yes {
			ok, err := confirm(fmt.Sprintf("Remove source %q from %q? Its existing snapshots stay in the repo.", args[0], cred.Name))
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("aborted")
			}
		}
		if err := s.DeleteSource(cmd.Context(), cred.ID, args[0]); errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no source named %q for %q", args[0], cred.Name)
		} else if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Removed source %q\n", args[0])
		return nil
	}
	return cmd
}
