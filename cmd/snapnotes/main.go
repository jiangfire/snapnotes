package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/jiangfire/snapnotes/internal/client"
	"github.com/jiangfire/snapnotes/internal/storage/sqlite"
	"github.com/jiangfire/snapnotes/internal/sync"
	"github.com/jiangfire/snapnotes/internal/tui"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "stream":
			// stream has its own subcommand (create); strip it before flag parse.
			if len(os.Args) < 3 {
				log.Fatal("usage: snapnotes stream create [flags]")
			}
			if os.Args[2] != "create" {
				log.Fatalf("unknown stream subcommand %q", os.Args[2])
			}
			if err := runStreamCreate(os.Args[3:]); err != nil {
				log.Fatal(err)
			}
			return
		case "key":
			if err := runKey(os.Args[2:]); err != nil {
				log.Fatal(err)
			}
			return
		}
	}
	runApp() // no subcommand: interactive TUI (legacy default)
}

// --- stream subcommand ----------------------------------------------------

func runStreamCreate(args []string) error {
	fs := flag.NewFlagSet("stream create", flag.ExitOnError)
	name := fs.String("name", "main", "human label for this stream")
	server := fs.String("server", "http://localhost:8333", "sync server endpoint")
	dataDir := fs.String("data-dir", ".snapnotes", "directory for the local database and device config")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := client.InitOwner(*server, nil)
	if err != nil {
		return fmt.Errorf("init stream: %w", err)
	}
	cfg.Name = *name

	if err := cfg.Save(filepath.Join(*dataDir, "config.json")); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	fmt.Printf("Stream %q created.\n", *name)
	fmt.Printf("Config saved to %s\n", filepath.Join(*dataDir, "config.json"))
	fmt.Println("Start the sync server with:")
	fmt.Printf("  snapnotes-server -genesis %s\n", cfg.GenesisBlock)
	fmt.Printf("Then run `snapnotes` (no args) to open the notebook against %s.\n", *server)
	return nil
}

// --- key subcommand -------------------------------------------------------

func runKey(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: snapnotes key <export|import> [flags]")
	}
	switch args[0] {
	case "export":
		return runKeyExport(args[1:])
	case "import":
		return runKeyImport(args[1:])
	default:
		return fmt.Errorf("unknown key subcommand %q", args[0])
	}
}

func runKeyExport(args []string) error {
	fs := flag.NewFlagSet("key export", flag.ExitOnError)
	dataDir := fs.String("data-dir", ".snapnotes", "directory holding config.json")
	output := fs.String("output", "snapnotes-key-backup.age", "output file for the encrypted backup")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := client.Load(filepath.Join(*dataDir, "config.json"))
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	pass, err := readPassphrase("Backup passphrase: ")
	if err != nil {
		return err
	}
	armored, err := client.ExportKeys(cfg, pass)
	if err != nil {
		return fmt.Errorf("export keys: %w", err)
	}
	if err := os.WriteFile(*output, armored, 0o600); err != nil {
		return fmt.Errorf("write backup: %w", err)
	}
	fmt.Printf("Encrypted key backup written to %s (age/scrypt). Keep it safe; the server never holds these keys.\n", *output)
	return nil
}

func runKeyImport(args []string) error {
	fs := flag.NewFlagSet("key import", flag.ExitOnError)
	dataDir := fs.String("data-dir", ".snapnotes", "directory to restore config.json into")
	input := fs.String("input", "snapnotes-key-backup.age", "encrypted backup file to restore")
	if err := fs.Parse(args); err != nil {
		return err
	}

	armored, err := os.ReadFile(*input)
	if err != nil {
		return fmt.Errorf("read backup: %w", err)
	}
	pass, err := readPassphrase("Backup passphrase: ")
	if err != nil {
		return err
	}
	cfg, err := client.ImportKeys(armored, pass)
	if err != nil {
		return fmt.Errorf("import keys: %w", err)
	}
	if err := cfg.Save(filepath.Join(*dataDir, "config.json")); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	fmt.Printf("Restored stream %q config to %s\n", cfg.Name, filepath.Join(*dataDir, "config.json"))
	return nil
}

// readPassphrase reads a single line from stdin. It intentionally does not
// disable terminal echo (cross-platform no-echo is fragile); the user should
// avoid pasting secrets where they could be logged. The encryption boundary is
// age/scrypt, not the terminal.
func readPassphrase(prompt string) (string, error) {
	fmt.Print(prompt)
	line, err := bufio.NewReader(io.Reader(os.Stdin)).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read passphrase: %w", err)
	}
	return trimNewline(line), nil
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// --- interactive app (default) -------------------------------------------

func runApp() {
	dataDir := flag.String("data-dir", ".snapnotes", "directory for the local database and device config")
	server := flag.String("server", "http://localhost:8333", "sync server endpoint (e.g. http://host:8333)")
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		log.Fatal(err)
	}

	cfg, err := loadOrCreateConfig(*dataDir, *server)
	if err != nil {
		log.Fatal(err)
	}

	store, err := sqlite.Open(filepath.Join(*dataDir, "snapnotes.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	keys, err := cfg.ClientKeys()
	if err != nil {
		log.Fatalf("client keys: %v", err)
	}
	anchor, err := cfg.TrustAnchor()
	if err != nil {
		log.Fatalf("trust anchor: %v", err)
	}

	endpoint := cfg.ServerEndpoint
	if endpoint == "" {
		endpoint = *server
	}

	noteSvc := &sync.NoteService{Store: store, Keys: keys}
	syncCli := &sync.SyncClient{
		Repo:      store,
		Endpoint:  endpoint,
		Anchor:    anchor,
		DeviceID:  cfg.DeviceID,
		StreamKey: keys.StreamKey,
		KeyEpoch:  keys.KeyEpoch,
	}

	home, err := tui.NewHomeModel(store, noteSvc, syncCli, nil, nil)
	if err != nil {
		log.Fatal(err)
	}
	model := tui.NewAppModel(home, store, syncCli)

	p := tea.NewProgram(model)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Periodic catch-up: the server only notifies, so the client pulls on a timer.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.Send(tui.SyncTickMsg{})
			}
		}
	}()

	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// loadOrCreateConfig reads the device config, or bootstraps a new owner stream
// on first run and prints the server command the user must start.
func loadOrCreateConfig(dataDir, server string) (client.Config, error) {
	path := filepath.Join(dataDir, "config.json")
	cfg, err := client.Load(path)
	if err == nil {
		return cfg, nil
	}
	if !os.IsNotExist(err) {
		return client.Config{}, fmt.Errorf("load config: %w", err)
	}

	cfg, err = client.InitOwner(server, nil)
	if err != nil {
		return client.Config{}, fmt.Errorf("init stream: %w", err)
	}
	if err := cfg.Save(path); err != nil {
		return client.Config{}, fmt.Errorf("save config: %w", err)
	}
	fmt.Println("New stream created. Start the sync server with:")
	fmt.Printf("  snapnotes-server -genesis %s\n", cfg.GenesisBlock)
	fmt.Printf("Then return to this terminal — notes will sync to %s.\n\n", cfg.ServerEndpoint)
	return cfg, nil
}
