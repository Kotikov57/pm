package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"pm/internal/config"
	"pm/internal/packager"
	"pm/internal/sshcmd"
	"pm/internal/updater"
)

func main() {
	log.SetFlags(0)
	if err := loadDotEnv(".env"); err != nil {
		log.Fatalf("failed to load .env file: %v", err)
	}
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "create":
		err = runCreate(args)
	case "update":
		err = runUpdate(args)
	case "help", "-h", "--help":
		usage()
		return
	default:
		usage()
		os.Exit(1)
	}

	if err != nil {
		log.Fatalf("%v", err)
	}
}

func usage() {
	fmt.Println(`Usage:
  pm create <spec> [flags]
  pm update <spec> [flags]

Flags:
  --ssh-host       SSH host (can use PM_SSH_HOST)
  --ssh-port       SSH port (default 22 or PM_SSH_PORT)
  --ssh-user       SSH user (PM_SSH_USER)
  --ssh-key        Path to private key (PM_SSH_KEY)
  --remote-dir     Remote directory for archives (PM_REMOTE_DIR)
  --output         Output archive path (create command)
  --local-dir      Destination directory (update command, default current)`)
}

func runCreate(args []string) error {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)

	sshHost := fs.String("ssh-host", getenv("PM_SSH_HOST", ""), "SSH host")
	sshPort := fs.Int("ssh-port", getenvInt("PM_SSH_PORT", 22), "SSH port")
	sshUser := fs.String("ssh-user", getenv("PM_SSH_USER", ""), "SSH user")
	sshKey := fs.String("ssh-key", getenv("PM_SSH_KEY", defaultSSHKeyPath()), "SSH private key")
	remoteDir := fs.String("remote-dir", getenv("PM_REMOTE_DIR", ""), "Remote directory")
	outputPath := fs.String("output", "", "Output archive path")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() < 1 {
		return fmt.Errorf("missing package spec path")
	}
	specPath := fs.Arg(0)

	spec, err := config.LoadPackageSpec(specPath)
	if err != nil {
		return err
	}

	archivePath, manifest, err := packager.Create(spec, packager.CreateOptions{OutputPath: *outputPath})
	if err != nil {
		return err
	}

	fmt.Printf("Created archive %s containing %d files\n", archivePath, len(manifest.Files))

	if *sshHost == "" {
		fmt.Println("SSH host not provided, skipping upload")
		return nil
	}

	cfg := sshcmd.Config{
		Host:     *sshHost,
		Port:     *sshPort,
		User:     *sshUser,
		Identity: *sshKey,
	}
	remotePath, err := sshcmd.UploadFile(cfg, archivePath, *remoteDir)
	if err != nil {
		return err
	}

	fmt.Printf("Uploaded to %s\n", remotePath)
	return nil
}

func runUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)

	sshHost := fs.String("ssh-host", getenv("PM_SSH_HOST", ""), "SSH host")
	sshPort := fs.Int("ssh-port", getenvInt("PM_SSH_PORT", 22), "SSH port")
	sshUser := fs.String("ssh-user", getenv("PM_SSH_USER", ""), "SSH user")
	sshKey := fs.String("ssh-key", getenv("PM_SSH_KEY", defaultSSHKeyPath()), "SSH private key")
	remoteDir := fs.String("remote-dir", getenv("PM_REMOTE_DIR", ""), "Remote directory")
	localDir := fs.String("local-dir", ".", "Local extraction directory")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() < 1 {
		return fmt.Errorf("missing update spec path")
	}
	specPath := fs.Arg(0)

	if *sshHost == "" {
		return fmt.Errorf("ssh host is required for update")
	}
	spec, err := config.LoadUpdateSpec(specPath)
	if err != nil {
		return err
	}

	cfg := sshcmd.Config{
		Host:     *sshHost,
		Port:     *sshPort,
		User:     *sshUser,
		Identity: *sshKey,
	}

	results, err := updater.Update(spec, updater.UpdateOptions{
		RemoteDir: *remoteDir,
		LocalDir:  *localDir,
		SSH:       cfg,
	})
	if err != nil {
		return err
	}

	for _, res := range results {
		manifestInfo := ""
		if res.Manifest != "" {
			manifestInfo = fmt.Sprintf(", manifest %s", res.Manifest)
		}
		fmt.Printf("Downloaded %s %s to %s (archive %s%s)\n", res.PackageName, res.Version, res.ExtractedTo, res.ArchivePath, manifestInfo)
	}
	return nil
}

func getenv(key, def string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return def
}

func getenvInt(key string, def int) int {
	if val := os.Getenv(key); val != "" {
		if v, err := strconv.Atoi(val); err == nil {
			return v
		}
	}
	return def
}

func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		idx := strings.IndexRune(line, '=')
		if idx == -1 {
			continue
		}

		key := strings.TrimSpace(line[:idx])
		if key == "" {
			continue
		}

		value := strings.TrimSpace(line[idx+1:])
		if len(value) >= 2 {
			if (strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"")) ||
				(strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'")) {
				value = value[1 : len(value)-1]
			}
		}

		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}

	return scanner.Err()
}

func defaultSSHKeyPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	candidates := []string{
		filepath.Join(home, ".ssh", "id_ed25519"),
		filepath.Join(home, ".ssh", "id_rsa"),
		filepath.Join(home, ".ssh", "id_ecdsa"),
		filepath.Join(home, ".ssh", "id_dsa"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}
