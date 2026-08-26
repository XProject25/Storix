package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/XProject25/Storix/internal/auth"
	"github.com/XProject25/Storix/internal/store"
	"github.com/XProject25/Storix/internal/vfs"
)

// cmdSetup completes the first run without a browser.
//
// The wizard is the right answer for a person at a keyboard, but a server is
// often built by a script, and a script cannot open a link. Without this,
// creating an administrator with "storix user add" left the account unable to
// sign in, because nothing had marked the install as set up, and the only
// symptom was every request answering "Storix has not been set up yet".
func cmdSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	cfgPath, port, data := commonFlags(fs)
	username := fs.String("username", "admin", "administrator username")
	password := fs.String("password", "", "administrator password, read from STORIX_PASSWORD or generated when empty")
	display := fs.String("name", "", "display name")
	email := fs.String("email", "", "email address")
	domain := fs.String("domain", "", "domain to serve on, enables automatic HTTPS")
	var folders multiFlag
	fs.Var(&folders, "folder", "folder Storix may manage, repeatable")
	flagArgs, plain := splitArgs(args, map[string]bool{
		"username": true, "password": true, "name": true, "email": true,
		"domain": true, "folder": true, "config": true, "port": true, "data": true,
	})
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if len(plain) > 0 {
		return fmt.Errorf("unexpected argument %q, every value needs its flag", plain[0])
	}

	a, err := open(*cfgPath, *port, *data, true)
	if err != nil {
		return err
	}
	defer a.close()
	ctx := context.Background()

	if a.store.SetupCompleted(ctx) {
		return errors.New("this server is already set up, use storix user add to create another account")
	}

	name := strings.TrimSpace(*username)
	if err := validUsername(name); err != nil {
		return err
	}

	secret := *password
	if secret == "" {
		secret = os.Getenv("STORIX_PASSWORD")
	}
	generated := false
	if secret == "" {
		secret = promptPassword("Password for " + name + ": ")
	}
	if secret == "" {
		secret = auth.MustToken(12)
		generated = true
	}
	if err := auth.ValidatePassword(secret); err != nil {
		return err
	}

	chosen, err := setupFolders(a, folders)
	if err != nil {
		return err
	}

	hash, err := auth.HashPassword(secret)
	if err != nil {
		return err
	}
	admin := &store.User{
		Username:     name,
		DisplayName:  strings.TrimSpace(*display),
		Email:        strings.TrimSpace(*email),
		PasswordHash: hash,
		Role:         store.RoleAdmin,
		Permissions:  store.PermissionsForRole(store.RoleAdmin),
		Active:       true,
		Theme:        "dark",
		Locale:       "en",
	}
	if admin.DisplayName == "" {
		admin.DisplayName = name
	}
	if _, err := a.store.CreateUser(ctx, admin); err != nil {
		return err
	}

	for i, folder := range chosen {
		root := &store.Root{Path: folder, Label: filepath.Base(folder), Icon: "folder", SortOrder: i}
		if root.Label == "" || root.Label == "/" {
			root.Label = "Root volume"
		}
		if _, err := a.store.CreateRoot(ctx, root); err != nil && !errors.Is(err, store.ErrConflict) {
			return fmt.Errorf("add folder %s: %w", folder, err)
		}
	}

	if d := strings.TrimSpace(*domain); d != "" {
		a.cfg.Server.Domain = d
		a.cfg.Server.TLS.Mode = "acme"
		a.cfg.Security.CookieSecure = true
		a.cfg.Normalize()
		if err := a.cfg.Save(); err != nil {
			fmt.Printf("Note: the domain could not be written to %s, set it in Settings instead\n", *cfgPath)
		}
	}

	if err := a.store.MarkSetupCompleted(ctx); err != nil {
		return err
	}
	if err := a.store.DeleteSetting(ctx, "setup.token"); err != nil {
		a.log.Warn("setup token not cleared", "err", err)
	}
	_ = os.Remove(filepath.Join(a.cfg.Storage.DataDir, "setup-token"))

	fmt.Printf("\n  Storix is set up.\n\n")
	fmt.Printf("  Sign in at %s\n", a.cfg.PublicURL())
	fmt.Printf("  Username   %s\n", name)
	if generated {
		fmt.Printf("  Password   %s\n", secret)
		fmt.Printf("\n  That password was generated because none was given. Change it with:\n")
		fmt.Printf("    storix user passwd %s\n", name)
	}
	fmt.Printf("\n  Folders    %s\n", strings.Join(chosen, ", "))
	if strings.TrimSpace(*domain) != "" {
		fmt.Printf("  Domain     %s, restart the service once DNS points here\n", strings.TrimSpace(*domain))
	}
	fmt.Printf("\n  Developed by X Project\n\n")
	return nil
}

// validUsername applies the same rule the wizard does.
func validUsername(name string) error {
	if len(name) < 2 || len(name) > 32 {
		return errors.New("a username needs between 2 and 32 characters")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '-', r == '_':
		default:
			return fmt.Errorf("a username may hold letters, digits, dots, dashes and underscores, not %q", r)
		}
	}
	return nil
}

// setupFolders checks the folders exist and are allowed, and falls back to the
// same default the wizard offers when none were named.
func setupFolders(a *app, given []string) ([]string, error) {
	cleaned := make([]string, 0, len(given))
	seen := make(map[string]bool, len(given))
	for _, folder := range given {
		p := vfs.Clean(folder)
		if p == "" || seen[p] {
			continue
		}
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("folder %s cannot be read: %w", p, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%s is not a folder", p)
		}
		if a.vfs.Denied(p) {
			return nil, fmt.Errorf("%s is a protected location and cannot be managed", p)
		}
		seen[p] = true
		cleaned = append(cleaned, p)
	}
	if len(cleaned) > 0 {
		return cleaned, nil
	}
	for _, fallback := range []string{"/home", "/srv", "/"} {
		if info, err := os.Stat(fallback); err == nil && info.IsDir() && !a.vfs.Denied(fallback) {
			return []string{fallback}, nil
		}
	}
	return nil, errors.New("name at least one folder with -folder, for example -folder /home")
}
