// photothing is a small, self-hosted photo gallery with accounts, MFA and RBAC.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"photothing/internal/auth"
	"photothing/internal/backup"
	"photothing/internal/gallery"
	"photothing/internal/store"
	"photothing/internal/web"
)

const usage = `usage: photothing [global flags] <command> [args]

commands:
  serve                       run the web server (default)
  scan                        rescan the photo directory and exit
  user add <name> <role>      create a user (role: viewer, editor, admin)
  user passwd <name>          set a new password
  user mfa-reset <name>       disable MFA for a user
  user list                   list users
  public on|off               allow anonymous viewing of the gallery
  backup [file]               write a backup archive (database + key); safe while the server runs
  restore [-force] <file>     restore an archive into -data (stop the server first)
  config                      show the effective settings (after --config, environment and flags)

Run several instances by giving each its own configuration file:
  photothing --config=/etc/photothing/familie.conf serve
  photothing --config=/etc/photothing/familie.conf user add ton admin

global flags (before the command):
`

func main() {
	cfg, args, err := parseArgs(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		os.Exit(0)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	if len(args) == 0 {
		args = []string{"serve"}
	}
	if err := run(cfg, args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(cfg config, args []string) error {
	if args[0] == "config" {
		return configCmd(cfg)
	}
	if err := os.MkdirAll(cfg.data, 0o700); err != nil {
		return err
	}
	if args[0] == "restore" { // must not open (and thereby create) the database first
		unlock, err := lockDataDir(cfg.data) // fails while the server runs
		if err != nil {
			return err
		}
		defer unlock()
		return restoreCmd(cfg, args[1:])
	}
	st, err := store.Open(filepath.Join(cfg.data, "photothing.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	switch args[0] {
	case "serve":
		return serve(cfg, st)
	case "scan":
		lib, err := openLibrary(cfg, st)
		if err != nil {
			return err
		}
		res, err := lib.Scan(context.Background())
		fmt.Println(res)
		return err
	case "backup":
		return backupCmd(cfg, st, args[1:])
	case "user":
		return userCmd(st, args[1:])
	case "public":
		if len(args) != 2 || (args[1] != "on" && args[1] != "off") {
			return errors.New("usage: public on|off")
		}
		v := "0"
		if args[1] == "on" {
			v = "1"
		}
		return st.SetSetting("public", v)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func openLibrary(cfg config, st *store.Store) (*gallery.Library, error) {
	return gallery.Open(cfg.photos, filepath.Join(cfg.data, "cache"), st)
}

func serve(cfg config, st *store.Store) error {
	unlock, err := lockDataDir(cfg.data)
	if err != nil {
		return err
	}
	defer unlock()
	lib, err := openLibrary(cfg, st)
	if err != nil {
		return err
	}
	box, err := auth.LoadBox(filepath.Join(cfg.data, "secret.key"))
	if err != nil {
		return err
	}
	trusted, err := web.ParseTrustedProxies(cfg.trustedProxy)
	if err != nil {
		return err
	}
	srv, err := web.New(st, lib, box, web.Options{SecureCookies: cfg.secure, TrustedProxies: trusted})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.configFile != "" {
		log.Printf("settings from %s", cfg.configFile)
	}
	if len(trusted) > 0 {
		log.Printf("trusting X-Forwarded-* headers from %s", cfg.trustedProxy)
	}
	log.Printf("optional format support: %s", lib.Tools)
	go housekeeping(ctx, cfg, st, lib)

	hs := &http.Server{
		Addr:              cfg.addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		hs.Shutdown(sc)
	}()
	log.Printf("listening on http://%s (photos: %s, data: %s)", cfg.addr, cfg.photos, cfg.data)
	if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// housekeeping runs the initial scan, periodic rescans, session purging and automatic backups.
func housekeeping(ctx context.Context, cfg config, st *store.Store, lib *gallery.Library) {
	scan := func(quiet bool) {
		res, err := lib.Scan(ctx)
		switch {
		case err != nil:
			log.Printf("scan: %v", err)
		case !quiet || res.Added+res.Updated+res.Moved+res.Removed > 0:
			log.Printf("scan: %s", res)
		}
	}
	scan(false)
	lib.PruneCache()

	every := func(d time.Duration) <-chan time.Time {
		if d <= 0 {
			return nil // a nil channel never fires
		}
		return time.Tick(d)
	}
	doBackup := func() {
		if cfg.backupDir == "" {
			return
		}
		if err := os.MkdirAll(cfg.backupDir, 0o700); err != nil {
			log.Printf("backup: %v", err)
			return
		}
		out := filepath.Join(cfg.backupDir, backup.DefaultName(time.Now()))
		if err := backup.Create(st, cfg.data, out); err != nil {
			log.Printf("backup: %v", err)
			return
		}
		if err := backup.Prune(cfg.backupDir, cfg.backupKeep); err != nil {
			log.Printf("backup prune: %v", err)
		}
		log.Printf("backup written: %s", out)
	}
	if cfg.backupDir != "" && time.Since(backup.Latest(cfg.backupDir)) >= cfg.backupEvery {
		doBackup()
	}
	scanTick, purgeTick, backupTick := every(cfg.scanEvery), every(time.Hour), every(cfg.backupEvery)
	if cfg.backupDir == "" {
		backupTick = nil
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-scanTick:
			scan(true)
		case <-purgeTick:
			if err := st.Purge(365 * 24 * time.Hour); err != nil {
				log.Printf("purge: %v", err)
			}
			lib.PruneCache()
		case <-backupTick:
			doBackup()
		}
	}
}

// configCmd prints the effective settings and checks the ones that can be checked without starting.
func configCmd(cfg config) error {
	for _, l := range cfg.lines() {
		fmt.Println(l)
	}
	if _, err := web.ParseTrustedProxies(cfg.trustedProxy); err != nil {
		return err
	}
	if cfg.backupKeep < 1 {
		return errors.New("backup-keep must be at least 1")
	}
	return nil
}

func backupCmd(cfg config, st *store.Store, args []string) error {
	out := backup.DefaultName(time.Now())
	if cfg.backupDir != "" {
		if err := os.MkdirAll(cfg.backupDir, 0o700); err != nil {
			return err
		}
		out = filepath.Join(cfg.backupDir, out)
	}
	if len(args) > 1 {
		return errors.New("usage: backup [file]")
	}
	if len(args) == 1 {
		out = args[0]
	}
	if err := backup.Create(st, cfg.data, out); err != nil {
		return err
	}
	info, _ := os.Stat(out)
	fmt.Printf("backup written: %s (%d bytes)\n", out, info.Size())
	fmt.Println("It contains password hashes and the key for the MFA seeds: store it securely.")
	return nil
}

func restoreCmd(cfg config, args []string) error {
	force := false
	if len(args) > 0 && args[0] == "-force" {
		force, args = true, args[1:]
	}
	if len(args) != 1 {
		return errors.New("usage: restore [-force] <file>")
	}
	if err := backup.Restore(args[0], cfg.data, force); err != nil {
		return err
	}
	fmt.Printf("restored %s into %s\n", args[0], cfg.data)
	return nil
}

func userCmd(st *store.Store, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: user add|passwd|mfa-reset|list")
	}
	switch args[0] {
	case "list":
		users, err := st.ListUsers()
		if err != nil {
			return err
		}
		for _, u := range users {
			fmt.Printf("%-20s %-7s mfa=%-5v disabled=%v\n", u.Username, u.Role, u.TOTPEnabled, u.Disabled)
		}
		return nil
	case "add":
		if len(args) != 3 {
			return errors.New("usage: user add <name> <viewer|editor|admin>")
		}
		name, role := strings.TrimSpace(args[1]), args[2]
		if name == "" || !auth.ValidRole(role) {
			return errors.New("need a non-empty name and a role of viewer, editor or admin")
		}
		pw, err := readPassword()
		if err != nil {
			return err
		}
		hash, err := auth.HashPassword(pw)
		if err != nil {
			return err
		}
		if err := st.CreateUser(name, hash, role); err != nil {
			return err
		}
		fmt.Printf("created %s (%s)\n", name, role)
		return nil
	case "passwd", "mfa-reset":
		if len(args) != 2 {
			return fmt.Errorf("usage: user %s <name>", args[0])
		}
		u, err := st.UserByName(args[1])
		if err != nil {
			return fmt.Errorf("user %q: %w", args[1], err)
		}
		if args[0] == "mfa-reset" {
			if err := st.SetTOTP(u.ID, "", false); err != nil {
				return err
			}
			return st.DeleteUserSessions(u.ID)
		}
		pw, err := readPassword()
		if err != nil {
			return err
		}
		hash, err := auth.HashPassword(pw)
		if err != nil {
			return err
		}
		return st.SetPassword(u.ID, hash) // also revokes sessions
	default:
		return fmt.Errorf("unknown user command %q", args[0])
	}
}

// readPassword prompts without echo on a terminal, or reads one line from a pipe.
func readPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, "Password: ")
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		fmt.Fprint(os.Stderr, "Repeat:   ")
		b2, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		if string(b) != string(b2) {
			return "", errors.New("passwords do not match")
		}
		return string(b), nil
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
