package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/XProject25/Storix/internal/auth"
	"github.com/XProject25/Storix/internal/build"
	"github.com/XProject25/Storix/internal/config"
	"github.com/XProject25/Storix/internal/store"
	"github.com/XProject25/Storix/internal/updater"
	"github.com/XProject25/Storix/internal/vfs"
)

// ---- version ----------------------------------------------------------------

func cmdVersion() error {
	info := build.Current()
	fmt.Printf("%s %s\n", info.Product, info.Version)
	fmt.Printf("commit    %s\n", info.Commit)
	fmt.Printf("built     %s\n", info.Date)
	fmt.Printf("platform  %s\n", info.Platform)
	fmt.Printf("go        %s\n", info.GoVersion)
	fmt.Printf("developed by %s\n", info.Developer)
	return nil
}

// ---- accounts ---------------------------------------------------------------

func cmdUser(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: storix user add|list|passwd|disable|enable|delete")
	}
	sub := args[0]
	args = args[1:]

	fs := flag.NewFlagSet("user "+sub, flag.ContinueOnError)
	cfgPath, _, data := commonFlags(fs)
	role := fs.String("role", "user", "role: admin, manager, user, readonly")
	email := fs.String("email", "", "email address")
	display := fs.String("name", "", "display name")
	password := fs.String("password", "", "password, read from STORIX_PASSWORD or prompted when empty")
	var mounts multiFlag
	fs.Var(&mounts, "mount", "folder the account may use, repeatable")
	// The standard parser stops at the first plain word, so
	// "user passwd alice -password secret" would treat the flag as another
	// name and fail. People write the name first, so reorder before parsing.
	flags, rest := splitArgs(args, map[string]bool{"mount": true, "role": true, "email": true,
		"name": true, "password": true, "config": true, "port": true, "data": true})
	if err := fs.Parse(flags); err != nil {
		return err
	}
	rest = append(rest, fs.Args()...)

	a, err := open(*cfgPath, 0, *data, true)
	if err != nil {
		return err
	}
	defer a.close()
	ctx := context.Background()

	switch sub {
	case "list":
		return listUsers(ctx, a)
	case "add":
		if len(rest) != 1 {
			return errors.New("usage: storix user add <username> [-role admin] [-mount /home/john]")
		}
		return addUser(ctx, a, rest[0], *role, *email, *display, *password, mounts)
	case "passwd":
		if len(rest) != 1 {
			return errors.New("usage: storix user passwd <username>")
		}
		return changePassword(ctx, a, rest[0], *password)
	case "disable", "enable":
		if len(rest) != 1 {
			return fmt.Errorf("usage: storix user %s <username>", sub)
		}
		user, err := a.store.GetUserByName(ctx, rest[0])
		if err != nil {
			return err
		}
		if err := a.store.SetUserActive(ctx, user.ID, sub == "enable"); err != nil {
			return err
		}
		if sub == "disable" {
			_ = a.store.DeleteUserSessions(ctx, user.ID)
		}
		fmt.Printf("Account %s is now %sd\n", user.Username, sub)
		return nil
	case "delete":
		if len(rest) != 1 {
			return errors.New("usage: storix user delete <username>")
		}
		user, err := a.store.GetUserByName(ctx, rest[0])
		if err != nil {
			return err
		}
		if user.IsAdmin() {
			admins, err := a.store.CountAdmins(ctx)
			if err != nil {
				return err
			}
			if admins <= 1 {
				return errors.New("this is the last administrator, create another one first")
			}
		}
		if err := a.store.DeleteUser(ctx, user.ID); err != nil {
			return err
		}
		fmt.Printf("Account %s deleted\n", user.Username)
		return nil
	}
	return fmt.Errorf("unknown user command %q", sub)
}

func listUsers(ctx context.Context, a *app) error {
	users, err := a.store.ListUsers(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "USERNAME\tROLE\tACTIVE\t2FA\tFOLDERS\tLAST LOGIN")
	for _, u := range users {
		last := "never"
		if u.LastLoginAt != nil {
			last = u.LastLoginAt.Local().Format("2006-01-02 15:04")
		}
		folders := len(u.Mounts)
		scope := fmt.Sprintf("%d", folders)
		if u.IsAdmin() {
			scope = "all"
		}
		fmt.Fprintf(w, "%s\t%s\t%t\t%t\t%s\t%s\n", u.Username, u.Role, u.Active, u.TOTPEnabled, scope, last)
	}
	return w.Flush()
}

func addUser(ctx context.Context, a *app, username, role, email, display, password string, mounts []string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return errors.New("a username is required")
	}
	r := store.Role(strings.ToLower(strings.TrimSpace(role)))
	if !r.Valid() {
		return fmt.Errorf("unknown role %q", role)
	}
	generated := false
	if password == "" {
		password = os.Getenv("STORIX_PASSWORD")
	}
	if password == "" {
		password = promptPassword("Password for " + username + ": ")
	}
	if password == "" {
		password = auth.MustToken(12)
		generated = true
	}
	if err := auth.ValidatePassword(password); err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}

	user := &store.User{
		Username:     username,
		DisplayName:  strings.TrimSpace(display),
		Email:        strings.TrimSpace(email),
		PasswordHash: hash,
		Role:         r,
		Permissions:  store.PermissionsForRole(r),
		Active:       true,
		Theme:        "dark",
		Locale:       "en",
	}
	if user.DisplayName == "" {
		user.DisplayName = username
	}
	id, err := a.store.CreateUser(ctx, user)
	if err != nil {
		return err
	}
	if len(mounts) > 0 {
		list := make([]store.Mount, 0, len(mounts))
		for i, m := range mounts {
			clean := vfs.Clean(m)
			list = append(list, store.Mount{
				UserID:    id,
				Path:      clean,
				Label:     filepath.Base(clean),
				Icon:      "folder",
				SortOrder: i,
			})
		}
		if err := a.store.SetUserMounts(ctx, id, list); err != nil {
			return err
		}
	}
	fmt.Printf("Account %s created with role %s\n", username, r)
	if generated {
		fmt.Printf("Generated password: %s\n", password)
	}
	// An account created before the first run is finished cannot sign in, and
	// the only symptom is every request answering that Storix is not set up.
	// Say so here rather than letting somebody discover it at the login form.
	if !a.store.SetupCompleted(ctx) {
		fmt.Printf("\nThis server has not finished its first run, so no account can sign in yet.\n")
		fmt.Printf("Finish it without a browser:\n")
		fmt.Printf("  storix setup -username %s -folder /home\n", username)
		fmt.Printf("or open the link from: storix setup-token\n")
	}
	return nil
}

func changePassword(ctx context.Context, a *app, username, password string) error {
	user, err := a.store.GetUserByName(ctx, username)
	if err != nil {
		return err
	}
	if password == "" {
		password = os.Getenv("STORIX_PASSWORD")
	}
	generated := false
	if password == "" {
		password = promptPassword("New password for " + username + ": ")
	}
	if password == "" {
		password = auth.MustToken(12)
		generated = true
	}
	if err := auth.ValidatePassword(password); err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if err := a.store.SetPassword(ctx, user.ID, hash, false); err != nil {
		return err
	}
	// Every existing session is invalidated so a stolen cookie stops working.
	_ = a.store.DeleteUserSessions(ctx, user.ID)
	fmt.Printf("Password updated for %s\n", user.Username)
	if generated {
		fmt.Printf("Generated password: %s\n", password)
	}
	return nil
}

// promptPassword reads a password without echoing it. It returns an empty
// string when stdin is not a terminal, so scripted use falls back cleanly.
func promptPassword(prompt string) string {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return ""
	}
	fmt.Print(prompt)
	data, err := term.ReadPassword(fd)
	fmt.Println()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }

func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// ---- setup token ------------------------------------------------------------

func cmdSetupToken(args []string) error {
	fs := flag.NewFlagSet("setup-token", flag.ContinueOnError)
	cfgPath, _, data := commonFlags(fs)
	reset := fs.Bool("reset", false, "issue a new token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a, err := open(*cfgPath, 0, *data, true)
	if err != nil {
		return err
	}
	defer a.close()
	ctx := context.Background()

	if a.store.SetupCompleted(ctx) && !*reset {
		fmt.Println("Storix is already set up. Sign in with an existing account,")
		fmt.Println("or run: storix user passwd <username>")
		return nil
	}
	token, err := a.store.GetSetting(ctx, "setup.token")
	if err != nil {
		return err
	}
	if token == "" || *reset {
		token = auth.MustToken(18)
		if err := a.store.SetSetting(ctx, "setup.token", token); err != nil {
			return err
		}
	}
	fmt.Printf("%s/setup?token=%s\n", a.cfg.PublicURL(), token)
	return nil
}

// ---- update -----------------------------------------------------------------

func cmdUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	cfgPath, _, data := commonFlags(fs)
	checkOnly := fs.Bool("check", false, "only report whether an update exists")
	channel := fs.String("channel", "", "release channel: stable or beta")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a, err := open(*cfgPath, 0, *data, true)
	if err != nil {
		return err
	}
	defer a.close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()

	ch := *channel
	if ch == "" {
		ch, _ = a.store.GetSetting(ctx, store.SettingUpdateChannel)
	}
	up := updater.New(updater.Options{Channel: ch, Logger: a.log})
	rel, err := up.Check(ctx)
	if err != nil {
		return err
	}
	if !rel.Available {
		fmt.Printf("Storix %s is the newest version\n", rel.Current)
		return nil
	}
	fmt.Printf("Update available: %s -> %s\n", rel.Current, rel.Version)
	if rel.Notes != "" {
		fmt.Println()
		fmt.Println(strings.TrimSpace(rel.Notes))
		fmt.Println()
	}
	if *checkOnly {
		return nil
	}
	if !rel.Writable {
		return fmt.Errorf("cannot write %s, run this command with sudo", up.BinaryPath())
	}

	last := -1
	err = up.Apply(ctx, rel, func(done, total int64) {
		if total <= 0 {
			return
		}
		pct := int(done * 100 / total)
		if pct != last && pct%5 == 0 {
			last = pct
			fmt.Printf("\rDownloading %3d%%", pct)
		}
	})
	fmt.Println()
	if err != nil {
		return err
	}
	fmt.Printf("Storix %s installed. Restart the service to run it:\n", rel.Version)
	fmt.Println("  sudo systemctl restart storix")
	return nil
}

// ---- doctor -----------------------------------------------------------------

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	cfgPath, port, data := commonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	ok, warn, bad := 0, 0, 0
	report := func(state, message string) {
		switch state {
		case "ok":
			ok++
			fmt.Printf("  [ ok ]   %s\n", message)
		case "warn":
			warn++
			fmt.Printf("  [warn]   %s\n", message)
		default:
			bad++
			fmt.Printf("  [fail]   %s\n", message)
		}
	}

	fmt.Printf("Storix %s on %s\n\n", build.Version, build.Platform())

	if _, err := os.Stat(*cfgPath); err == nil {
		report("ok", "configuration found at "+*cfgPath)
	} else {
		report("warn", "no configuration at "+*cfgPath+", built in defaults are in use")
	}

	a, err := open(*cfgPath, *port, *data, true)
	if err != nil {
		report("fail", "cannot open the installation: "+err.Error())
		fmt.Println()
		return err
	}
	defer a.close()
	ctx := context.Background()

	report("ok", "database opened at "+a.cfg.Storage.Database)

	probe := filepath.Join(a.cfg.Storage.DataDir, ".storix-doctor")
	if f, err := os.Create(probe); err == nil {
		f.Close()
		_ = os.Remove(probe)
		report("ok", "data directory is writable: "+a.cfg.Storage.DataDir)
	} else {
		report("fail", "data directory is not writable: "+a.cfg.Storage.DataDir)
	}

	addr := a.cfg.Addr()
	if ln, err := net.Listen("tcp", addr); err == nil {
		ln.Close()
		report("ok", "port "+addr+" is free, the service is not running")
	} else {
		report("ok", "port "+addr+" is in use, the service looks to be running")
	}

	if a.store.SetupCompleted(ctx) {
		report("ok", "first run wizard has been completed")
	} else {
		report("warn", "setup is not finished, run storix setup-token for the link")
	}

	admins, err := a.store.CountAdmins(ctx)
	if err != nil {
		report("fail", "cannot count administrators: "+err.Error())
	} else if admins == 0 {
		report("fail", "there is no active administrator, run storix user add <name> -role admin")
	} else {
		report("ok", fmt.Sprintf("%d administrator account(s)", admins))
	}

	roots, err := a.store.ListRoots(ctx)
	if err != nil {
		report("fail", "cannot read the folder list: "+err.Error())
	} else if len(roots) == 0 {
		report("warn", "no folders have been added yet")
	} else {
		for _, r := range roots {
			if info, err := os.Stat(r.Path); err != nil {
				report("fail", "folder is missing: "+r.Path)
			} else if !info.IsDir() {
				report("fail", "not a folder: "+r.Path)
			} else {
				usage, err := a.vfs.Disk(vfs.Scope{Admin: true, Mounts: []vfs.Mount{{Path: r.Path}}}, r.Path)
				if err != nil || usage == nil || usage.Total == 0 {
					report("ok", "folder available: "+r.Path)
				} else {
					report("ok", fmt.Sprintf("folder available: %s (%.0f%% used)", r.Path, usage.Percent))
				}
			}
		}
	}

	// Judge the binary by its own owner and mode, not by whoever ran this
	// command. Running doctor with sudo would otherwise always look unsafe.
	guard := updater.New(updater.Options{}).Protection()
	switch {
	case !guard.Known:
		report("warn", "cannot determine who owns "+guard.Path)
	case guard.OwnedByRoot && !guard.OthersWrite:
		report("ok", "the binary is owned by root and cannot be rewritten by the service")
	case guard.OthersWrite:
		report("fail", "the binary at "+guard.Path+" is writable by others, run: sudo chmod 0755 "+guard.Path)
	default:
		report("warn", "the binary at "+guard.Path+" is not owned by root, a root owned binary is safer")
	}

	fmt.Printf("\n%d ok, %d warnings, %d problems\n", ok, warn, bad)
	if bad > 0 {
		return errors.New("the installation needs attention")
	}
	return nil
}

// ---- config -----------------------------------------------------------------

func cmdConfig(args []string) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	cfgPath, port, data := commonFlags(fs)
	asJSON := fs.Bool("json", false, "print as JSON")
	initFile := fs.Bool("init", false, "write a default configuration file when none exists")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if *initFile {
		if _, statErr := os.Stat(*cfgPath); statErr == nil {
			fmt.Printf("Keeping the existing configuration at %s\n", *cfgPath)
			return nil
		}
		cfg.SetPath(*cfgPath)
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("Wrote %s\n", *cfgPath)
		return nil
	}
	if *port > 0 {
		cfg.Server.Port = *port
	}
	if *data != "" {
		cfg.Storage.DataDir = *data
		cfg.Normalize()
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"path":      *cfgPath,
			"publicUrl": cfg.PublicURL(),
			"server":    cfg.Server,
			"storage":   cfg.Storage,
			"limits":    cfg.Limits,
			"log":       cfg.Log,
		})
	}
	lines := []string{
		"config file    " + *cfgPath,
		"public url     " + cfg.PublicURL(),
		"listen         " + cfg.Addr(),
		"tls mode       " + string(cfg.Server.TLS.Mode),
		"data dir       " + cfg.Storage.DataDir,
		"database       " + cfg.Storage.Database,
		"trash          " + cfg.Storage.TrashDir,
		"log file       " + cfg.Log.File,
	}
	for _, l := range lines {
		fmt.Println(l)
	}
	return nil
}

// splitArgs separates flags from plain words so the two can be written in any
// order. The standard flag parser stops at the first plain word, which would
// turn "user passwd alice -password secret" into three names and an error.
// valued names the flags that take a following value, so "-mount /srv" is kept
// together while a boolean flag is not paired with the word after it.
func splitArgs(args []string, valued map[string]bool) (flags, plain []string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			plain = append(plain, arg)
			continue
		}
		if arg == "--" {
			// Everything after this is plain by convention.
			plain = append(plain, args[i+1:]...)
			return flags, plain
		}
		flags = append(flags, arg)
		name := strings.TrimLeft(arg, "-")
		if strings.Contains(name, "=") {
			// Already carries its value, for example -role=admin.
			continue
		}
		if valued[name] && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return flags, plain
}
