package main

import (
	"bufio"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const basePort = 3000

type PortMapping struct {
	App  string
	Port int
}

// buildPortmap discovers apps, assigns stable ports, and persists the mapping.
func buildPortmap(cfg Config) []PortMapping {
	existing := loadPortmap(cfg.PortmapFile)

	// Migrate legacy portmap from projects dir if new one is empty
	if len(existing) == 0 {
		legacy := filepath.Join(cfg.ProjectsDir, ".dot-test")
		if legacyMappings := loadPortmap(legacy); len(legacyMappings) > 0 {
			existing = legacyMappings
		}
	}

	apps := findProjects(cfg.ProjectsDir)
	usedPorts := make(map[int]bool)
	existingMap := make(map[string]int)
	names := make(map[string]string)
	for _, app := range apps {
		name := sanitizeName(app)
		if previous, ok := names[name]; ok {
			fmt.Fprintf(os.Stderr, "hostname collision between %q and %q\n", previous, app)
			return nil
		}
		names[name] = app
		if port := detectHardcodedPort(cfg.ProjectsDir, app); port > 0 {
			if port > 65535 || usedPorts[port] {
				fmt.Fprintf(os.Stderr, "conflicting or invalid pinned port for %q\n", app)
				return nil
			}
			existingMap[app] = port
			usedPorts[port] = true
		}
	}
	// Preserve only conflict-free assignments after reserving all pinned ports.
	for _, pm := range existing {
		if _, pinned := existingMap[pm.App]; !pinned && pm.Port > 0 && pm.Port <= 65535 && !usedPorts[pm.Port] {
			existingMap[pm.App] = pm.Port
			usedPorts[pm.Port] = true
		}
	}
	for _, app := range apps {
		if _, ok := existingMap[app]; ok {
			continue
		}
		port := basePort
		for usedPorts[port] && port <= 65535 {
			port++
		}
		if port > 65535 {
			return nil
		}
		existingMap[app] = port
		usedPorts[port] = true
	}

	// Build sorted result, pruning apps no longer on disk
	appSet := make(map[string]bool)
	for _, a := range apps {
		appSet[a] = true
	}

	var result []PortMapping
	for _, app := range apps {
		if port, ok := existingMap[app]; ok && appSet[app] {
			result = append(result, PortMapping{App: app, Port: port})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].App < result[j].App })

	if !savePortmap(cfg.PortmapFile, result) {
		return nil
	}
	return result
}

func loadPortmap(path string) []PortMapping {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var result []PortMapping
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		port, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		result = append(result, PortMapping{App: parts[0], Port: port})
	}
	return result
}

func savePortmap(path string, mappings []PortMapping) bool {
	var lines []string
	for _, pm := range mappings {
		lines = append(lines, fmt.Sprintf("%s=%d", pm.App, pm.Port))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return false
	}
	return true
}

// updateEnv sets PORT=<port> in the app's .env file.
func updateEnv(projectsDir, app string, port int) bool {
	if app == "" || strings.ContainsAny(app, `/\`) || strings.Contains(app, "..") || port < 1 || port > 65535 {
		fmt.Fprintf(os.Stderr, "skipping %q: invalid app name or port\n", app)
		return false
	}
	projects, err := os.OpenRoot(projectsDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return false
	}
	defer projects.Close()
	// Rooted operations reject symlink and junction escapes during resolution.
	project, err := projects.OpenRoot(app)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skipping %q: %v\n", app, err)
		return false
	}
	defer project.Close()
	content, err := project.ReadFile(".env")
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "cannot read .env for %q: %v\n", app, err)
		return false
	}
	portLine := fmt.Sprintf("PORT=%d", port)
	lines := strings.Split(string(content), "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(line, "PORT=") {
			lines[i] = portLine
			found = true
			break
		}
	}
	if !found {
		if lines[len(lines)-1] == "" {
			lines = append(lines[:len(lines)-1], portLine, "")
		} else {
			lines = append(lines, portLine)
		}
	}
	// Replace the directory entry atomically instead of truncating a possible hard link.
	temporary := fmt.Sprintf(".dot-test-env-%s", rand.Text())
	file, err := project.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return false
	}
	defer func() {
		if err := project.Remove(temporary); err != nil && !os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, err)
		}
	}()
	_, writeErr := file.WriteString(strings.Join(lines, "\n"))
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		fmt.Fprintf(os.Stderr, "cannot write .env for %q: %v / %v\n", app, writeErr, closeErr)
		return false
	}
	if err := project.Rename(temporary, ".env"); err != nil {
		fmt.Fprintf(os.Stderr, "cannot replace .env for %q: %v\n", app, err)
		return false
	}
	return true
}

// checkEnvGaps reports apps that won't read .env automatically.
func checkEnvGaps(projectsDir string, mappings []PortMapping) []string {
	var gaps []string
	for _, pm := range mappings {
		if detectHardcodedPort(projectsDir, pm.App) > 0 {
			continue
		}
		appDir := filepath.Join(projectsDir, pm.App)
		gemfile := filepath.Join(appDir, "Gemfile")
		content, err := os.ReadFile(gemfile)
		if err != nil {
			continue
		}

		hasDotenv := strings.Contains(string(content), "dotenv")
		hasForeman := fileExists(filepath.Join(appDir, "Procfile.dev"))

		if !hasForeman {
			if binDev, err := os.ReadFile(filepath.Join(appDir, "bin", "dev")); err == nil {
				s := string(binDev)
				if strings.Contains(s, "foreman") || strings.Contains(s, "overmind") {
					hasForeman = true
				}
			}
		}

		if !hasDotenv && !hasForeman {
			gaps = append(gaps, pm.App)
		}
	}
	return gaps
}
