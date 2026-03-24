package main

import (
	"bufio"
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

	for _, pm := range existing {
		usedPorts[pm.Port] = true
	}

	existingMap := make(map[string]int)
	for _, pm := range existing {
		existingMap[pm.App] = pm.Port
	}

	// First pass: honor hardcoded ports
	for _, app := range apps {
		if port := detectHardcodedPort(cfg.ProjectsDir, app); port > 0 {
			existingMap[app] = port
			usedPorts[port] = true
		}
	}

	// Second pass: assign ports to the rest
	for _, app := range apps {
		if _, ok := existingMap[app]; ok {
			continue
		}
		port := basePort
		for usedPorts[port] {
			port++
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

	savePortmap(cfg.PortmapFile, result)
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

func savePortmap(path string, mappings []PortMapping) {
	var lines []string
	for _, pm := range mappings {
		lines = append(lines, fmt.Sprintf("%s=%d", pm.App, pm.Port))
	}
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644)
}

// updateEnv sets PORT=<port> in the app's .env file.
func updateEnv(projectsDir, app string, port int) {
	envPath := filepath.Join(projectsDir, app, ".env")
	portLine := fmt.Sprintf("PORT=%d", port)

	if content, err := os.ReadFile(envPath); err == nil {
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
			// Append before any trailing empty line
			if lines[len(lines)-1] == "" {
				lines = append(lines[:len(lines)-1], portLine, "")
			} else {
				lines = append(lines, portLine)
			}
		}
		os.WriteFile(envPath, []byte(strings.Join(lines, "\n")), 0644)
	} else {
		os.WriteFile(envPath, []byte(portLine+"\n"), 0644)
	}
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
