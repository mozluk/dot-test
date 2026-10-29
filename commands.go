package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

func cmdSync() {
	cfg := loadConfig()
	mappings := buildPortmap(cfg)
	if len(mappings) == 0 {
		fmt.Fprintf(os.Stderr, "No projects found in %s\n", cfg.ProjectsDir)
		os.Exit(1)
	}
	if !saveProjectsDir(cfg.ProjectsDir) {
		os.Exit(1)
	}

	maxName := 0
	for _, pm := range mappings {
		n := len(sanitizeName(pm.App))
		if n > maxName {
			maxName = n
		}
	}

	fmt.Printf("%d projects:\n\n", len(mappings))
	for _, pm := range mappings {
		name := sanitizeName(pm.App)
		pinned := ""
		if detectHardcodedPort(cfg.ProjectsDir, pm.App) > 0 {
			pinned = " (pinned)"
		}
		url := fmt.Sprintf("  http://%s.test", name)
		fmt.Printf("%-*s -> localhost:%d%s\n", maxName+16, url, pm.Port, pinned)
	}

	fmt.Println("\nUpdating .env files...")
	updated := 0
	for _, pm := range mappings {
		if updateEnv(cfg.ProjectsDir, pm.App, pm.Port) {
			updated++
		}
	}
	if updated != len(mappings) {
		fmt.Fprintf(os.Stderr, "%d of %d projects updated; sync incomplete\n", updated, len(mappings))
		os.Exit(1)
	}
	fmt.Printf("  %d updated\n", len(mappings))

	gaps := checkEnvGaps(cfg.ProjectsDir, mappings)
	if len(gaps) > 0 {
		fmt.Println("\nWarning: these apps won't read .env without dotenv-rails or foreman:")
		for _, app := range gaps {
			fmt.Printf("  %s\n", app)
		}
		fmt.Println("\n  Fix: add 'gem \"dotenv-rails\"' to their Gemfile, or start with:")
		fmt.Println("  PORT=<port> rails s")
	}

	fmt.Println("\nDone. Run 'dot-test up' to start the daemon.")
}

func cmdList() {
	cfg := loadConfig()
	mappings := loadPortmap(cfg.PortmapFile)
	if len(mappings) == 0 {
		fmt.Fprintf(os.Stderr, "No portmap found. Run 'dot-test sync' first.\n")
		os.Exit(1)
	}

	maxName := 0
	for _, pm := range mappings {
		n := len(sanitizeName(pm.App))
		if n > maxName {
			maxName = n
		}
	}

	for _, pm := range mappings {
		name := sanitizeName(pm.App)
		fmt.Printf("%-*s -> localhost:%d\n", maxName+8, name+".test", pm.Port)
	}
}

func cmdUp() {
	cfg := loadConfig()

	// Check if already running
	if pid := readPid(cfg.PidFile); pid > 0 {
		if processAlive(pid) {
			fmt.Printf("dot-test already running (PID %d)\n", pid)
			return
		}
	}

	if !fileExists(cfg.PortmapFile) {
		fmt.Fprintln(os.Stderr, "No portmap found. Run 'dot-test sync' first.")
		os.Exit(1)
	}

	// Fork into background
	if os.Getenv("DOT_TEST_FOREGROUND") == "1" {
		writePid(cfg.PidFile, os.Getpid())
		srv := newServer(cfg)
		if err := srv.run(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Start self in background
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to locate executable: %v\n", err)
		os.Exit(1)
	}
	// #nosec G204 -- the executed binary is our own os.Executable() path with a
	// fixed argument; it never originates from user or network input.
	cmd := exec.Command(exe, "up")
	cmd.Env = append(os.Environ(), "DOT_TEST_FOREGROUND=1")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start daemon: %v\n", err)
		os.Exit(1)
	}
	started := false
	for attempt := 0; attempt < 50; attempt++ {
		if readPid(cfg.PidFile) == cmd.Process.Pid {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.ProxyPort), 100*time.Millisecond)
			if err == nil {
				reportCleanupError(conn.Close())
				started = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !started {
		reportCleanupError(cmd.Process.Kill())
		reportCleanupError(cmd.Wait())
		fmt.Fprintln(os.Stderr, "Daemon failed to start; verify the DNS and proxy ports are available")
		os.Exit(1)
	}
	reportCleanupError(cmd.Process.Release())
	fmt.Printf("dot-test started (PID %d)\n", cmd.Process.Pid)
	fmt.Printf("  DNS:   127.0.0.1:%d (*.test -> 127.0.0.1)\n", cfg.DNSPort)
	fmt.Printf("  Proxy: :%d (hostname routing)\n", cfg.ProxyPort)
}

func cmdDown() {
	cfg := loadConfig()
	pid := readPid(cfg.PidFile)
	if pid <= 0 {
		fmt.Println("dot-test is not running")
		return
	}
	if !terminateProcess(pid) {
		fmt.Println("dot-test was not running (stale PID file)")
	} else {
		fmt.Println("dot-test stopped")
	}
	reportCleanupError(os.Remove(cfg.PidFile))
}

func cmdSetup() {
	if runtime.GOOS == "darwin" {
		setupDarwin()
	} else if runtime.GOOS == "linux" {
		setupLinux()
	} else {
		fmt.Fprintf(os.Stderr, "Unsupported OS: %s\n", runtime.GOOS)
		os.Exit(1)
	}
}

func setupDarwin() {
	cfg := loadConfig()
	resolverDir := "/etc/resolver"
	resolverFile := filepath.Join(resolverDir, "test")

	content := fmt.Sprintf("nameserver 127.0.0.1\nport %d\n", cfg.DNSPort)

	if fileExists(resolverFile) {
		existing, _ := os.ReadFile(resolverFile)
		if string(existing) == content {
			fmt.Println("/etc/resolver/test already configured")
			return
		}
	}

	fmt.Println("Creating /etc/resolver/test (requires sudo)...")
	// Create directory
	if err := exec.Command("sudo", "mkdir", "-p", resolverDir).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	// Write resolver file
	tmp, err := os.CreateTemp("", "dot-test-resolver-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	tmpFile := tmp.Name()
	defer os.Remove(tmpFile)
	if _, err := tmp.WriteString(content); err != nil {
		reportCleanupError(tmp.Close())
		fmt.Fprintln(os.Stderr, err)
		return
	}
	if err := tmp.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	// #nosec G204 -- "sudo cp <tmpfile> /etc/resolver/test" runs with fixed
	// arguments; tmpFile is a deterministic per-PID name written by this
	// process and resolverFile is a constant path, not external input.
	if err := exec.Command("sudo", "cp", tmpFile, resolverFile).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create %s: %v\n", resolverFile, err)
		reportCleanupError(os.Remove(tmpFile))
		os.Exit(1)
	}
	reportCleanupError(os.Remove(tmpFile))
	fmt.Printf("Created %s (DNS queries for *.test -> 127.0.0.1:%d)\n", resolverFile, cfg.DNSPort)
}

func setupLinux() {
	cfg := loadConfig()
	fmt.Println("For systemd-resolved, add to /etc/systemd/resolved.conf:")
	fmt.Printf("  [Resolve]\n  DNS=127.0.0.1:%d\n  Domains=~test\n", cfg.DNSPort)
	fmt.Println("\nThen: sudo systemctl restart systemd-resolved")
}

func cmdClean() {
	cfg := loadConfig()
	cmdDown()

	if fileExists(cfg.PortmapFile) {
		reportCleanupError(os.Remove(cfg.PortmapFile))
		fmt.Println("Port assignments removed")
	}

	// Clean up legacy portmap in projects dir if it exists
	if saved := savedProjectsDir(); saved != "" {
		legacy := filepath.Join(saved, ".dot-test")
		if fileExists(legacy) {
			reportCleanupError(os.Remove(legacy))
			fmt.Println("Legacy portmap removed")
		}
	}

	// Remove saved projects dir
	reportCleanupError(os.Remove(filepath.Join(configDir(), "projects_dir")))

	if runtime.GOOS == "darwin" {
		resolverFile := "/etc/resolver/test"
		if fileExists(resolverFile) {
			if err := exec.Command("sudo", "rm", resolverFile).Run(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return
			}
			fmt.Println("/etc/resolver/test removed")
		}
	}

	fmt.Println("All clean.")
}

// PID records include process creation identity to prevent killing a recycled PID.
type pidRecord struct {
	PID      int    `json:"pid"`
	Identity string `json:"identity"`
}

func readPid(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var record pidRecord
	if json.Unmarshal(data, &record) != nil || record.PID <= 1 || record.Identity == "" {
		return 0
	}
	if identity := processIdentity(record.PID); identity == "" || identity != record.Identity {
		return 0
	}
	return record.PID
}

func writePid(path string, pid int) {
	identity := processIdentity(pid)
	if identity == "" {
		return
	}
	data, err := json.Marshal(pidRecord{PID: pid, Identity: identity})
	if err != nil {
		return
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

// reportCleanupError reports unexpected failures from best-effort cleanup.
func reportCleanupError(err error) {
	if err == nil || os.IsNotExist(err) {
		return
	}
	if _, expectedChildExit := err.(*exec.ExitError); expectedChildExit {
		return
	}
	fmt.Fprintln(os.Stderr, err)
}
