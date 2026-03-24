package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

func cmdSync() {
	cfg := loadConfig()
	mappings := buildPortmap(cfg)
	if len(mappings) == 0 {
		fmt.Fprintf(os.Stderr, "No projects found in %s\n", cfg.ProjectsDir)
		os.Exit(1)
	}
	saveProjectsDir(cfg.ProjectsDir)

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
	for _, pm := range mappings {
		updateEnv(cfg.ProjectsDir, pm.App, pm.Port)
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
		if err := syscall.Kill(pid, 0); err == nil {
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
	exe, _ := os.Executable()
	cmd := exec.Command(exe, "up")
	cmd.Env = append(os.Environ(), "DOT_TEST_FOREGROUND=1")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start daemon: %v\n", err)
		os.Exit(1)
	}
	cmd.Process.Release()
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
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		fmt.Println("dot-test was not running (stale PID file)")
	} else {
		fmt.Println("dot-test stopped")
	}
	os.Remove(cfg.PidFile)
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
	exec.Command("sudo", "mkdir", "-p", resolverDir).Run()
	// Write resolver file
	tmpFile := fmt.Sprintf("/tmp/dot-test-resolver-%d", os.Getpid())
	os.WriteFile(tmpFile, []byte(content), 0644)
	if err := exec.Command("sudo", "cp", tmpFile, resolverFile).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create %s: %v\n", resolverFile, err)
		os.Remove(tmpFile)
		os.Exit(1)
	}
	os.Remove(tmpFile)
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
		os.Remove(cfg.PortmapFile)
		fmt.Println("Port assignments removed")
	}

	// Clean up legacy portmap in projects dir if it exists
	if saved := savedProjectsDir(); saved != "" {
		legacy := filepath.Join(saved, ".dot-test")
		if fileExists(legacy) {
			os.Remove(legacy)
			fmt.Println("Legacy portmap removed")
		}
	}

	// Remove saved projects dir
	os.Remove(filepath.Join(configDir(), "projects_dir"))

	if runtime.GOOS == "darwin" {
		resolverFile := "/etc/resolver/test"
		if fileExists(resolverFile) {
			exec.Command("sudo", "rm", resolverFile).Run()
			fmt.Println("/etc/resolver/test removed")
		}
	}

	fmt.Println("All clean.")
}

func readPid(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

func writePid(path string, pid int) {
	os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0644)
}
