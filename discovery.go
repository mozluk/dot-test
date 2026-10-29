package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var defaultPorts = map[int]bool{3000: true, 3001: true}

// findProjects discovers web projects in the given directory.
// Currently detects Rails apps (config/application.rb).
func findProjects(dir string) []string {
	var apps []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		appDir := filepath.Join(dir, e.Name())
		// Rails app detection
		if fileExists(filepath.Join(appDir, "config", "application.rb")) {
			apps = append(apps, e.Name())
		}
	}
	sort.Strings(apps)
	return apps
}

// detectHardcodedPort checks bin/dev and Procfile.dev for non-default port assignments.
func detectHardcodedPort(dir, app string) int {
	appDir := filepath.Join(dir, app)

	// Check bin/dev for PORT=XXXX
	if content, err := os.ReadFile(filepath.Join(appDir, "bin", "dev")); err == nil {
		s := string(content)
		if port := matchPort(s, `(?m)^\s*PORT=(\d+)`); port > 0 && !defaultPorts[port] {
			return port
		}
		if port := matchPort(s, `(?:-p|--port)\s+(\d+)`); port > 0 && !defaultPorts[port] {
			return port
		}
	}

	// Check Procfile.dev for -p XXXX on web process
	if content, err := os.ReadFile(filepath.Join(appDir, "Procfile.dev")); err == nil {
		s := string(content)
		if port := matchPort(s, `(?m)^web:.*(?:-p|--port)\s+(\d+)`); port > 0 && !defaultPorts[port] {
			return port
		}
	}

	// Check mothership/foreman TOML manifests for http bind address
	for _, manifest := range []string{"ship-manifest.dev.toml", "ship-manifest.toml", "Procfile.toml"} {
		if content, err := os.ReadFile(filepath.Join(appDir, manifest)); err == nil {
			s := string(content)
			// Match http = "host:port" or listen = "host:port"
			if port := matchPort(s, `(?:http|listen)\s*=\s*"[^"]*:(\d+)"`); port > 0 && !defaultPorts[port] {
				return port
			}
		}
	}

	// Check docker-compose.yml for port mappings on web service
	for _, compose := range []string{"docker-compose.yml", "docker-compose.dev.yml", "compose.yml"} {
		if content, err := os.ReadFile(filepath.Join(appDir, compose)); err == nil {
			s := string(content)
			if port := matchPort(s, `"(\d+):(?:3000|8080)"`); port > 0 && !defaultPorts[port] {
				return port
			}
		}
	}

	return 0
}

// sanitizeName converts a directory name to a valid hostname segment.
func sanitizeName(name string) string {
	re := regexp.MustCompile(`[^a-z0-9-]`)
	return re.ReplaceAllString(strings.ToLower(name), "-")
}

func matchPort(s, pattern string) int {
	re := regexp.MustCompile(pattern)
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return 0
	}
	port, err := strconv.Atoi(m[1])
	if err != nil || port < 1 || port > 65535 {
		return 0
	}
	return port
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
