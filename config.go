package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	ProjectsDir string
	ProxyPort   int
	DNSPort     int
	PortmapFile string
	PidFile     string
}

// configDir returns ~/.config/dot-test, creating it if needed.
func configDir() string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".config", "dot-test")
	if err := os.MkdirAll(dir, 0700); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	return dir
}

// savedProjectsDir reads the persisted projects directory, if any.
func savedProjectsDir() string {
	data, err := os.ReadFile(filepath.Join(configDir(), "projects_dir"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// saveProjectsDir persists the projects directory for future use.
func saveProjectsDir(dir string) bool {
	if err := os.WriteFile(filepath.Join(configDir(), "projects_dir"), []byte(dir+"\n"), 0600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return false
	}
	return true
}

func loadConfig() Config {
	dir := os.Getenv("DOT_TEST_DIR")
	if dir == "" {
		dir, _ = os.Getwd()
	}

	proxyPort := 80
	if p := os.Getenv("DOT_TEST_PORT"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			proxyPort = v
		}
	}

	dnsPort := 15353
	if p := os.Getenv("DOT_TEST_DNS_PORT"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			dnsPort = v
		}
	}

	cdir := configDir()
	return Config{
		ProjectsDir: dir,
		ProxyPort:   proxyPort,
		DNSPort:     dnsPort,
		PortmapFile: filepath.Join(cdir, "portmap"),
		PidFile:     filepath.Join(cdir, "pid"),
	}
}
