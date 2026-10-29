package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestPortParserRejectsOverflowAndOutOfRangeValues(t *testing.T) {
	for _, value := range []string{"0", "65536", "18446744073709555937", strings.Repeat("9", 100)} {
		if port := matchPort("PORT="+value, `PORT=(\d+)`); port != 0 {
			t.Fatalf("invalid port %q was accepted as %d", value, port)
		}
	}
	if port := matchPort("PORT=4321", `PORT=(\d+)`); port != 4321 {
		t.Fatalf("valid port rejected: %d", port)
	}
}

func TestPortmapPersistenceReportsWriteFailure(t *testing.T) {
	if savePortmap(t.TempDir(), []PortMapping{{App: "app", Port: 4321}}) {
		t.Fatal("writing a portmap over a directory reported success")
	}
}

func TestEnvUpdateDoesNotTruncateAnExternalHardLink(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "app")
	if err := os.Mkdir(app, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.env")
	if err := os.WriteFile(outside, []byte("TOKEN=unchanged\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(app, ".env")); err != nil {
		t.Fatal(err)
	}
	updateEnv(root, "app", 4321)
	content, err := os.ReadFile(outside)
	if err != nil || string(content) != "TOKEN=unchanged\n" {
		t.Fatalf("external hard link was modified: %v", err)
	}
	updated, err := os.ReadFile(filepath.Join(app, ".env"))
	if err != nil || !strings.Contains(string(updated), "PORT=4321") {
		t.Fatalf("app port was not updated: %v", err)
	}
}

func TestEnvUpdateRejectsAnEscapingProjectLink(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	link := filepath.Join(root, "app")
	var err error
	if runtime.GOOS == "windows" {
		err = exec.Command("cmd", "/d", "/c", "mklink", "/J", link, outside).Run()
	} else {
		err = os.Symlink(outside, link)
	}
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(outside, ".env")
	if err := os.WriteFile(path, []byte("PORT=1234\n"), 0600); err != nil {
		t.Fatal(err)
	}
	updateEnv(root, "app", 4321)
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "PORT=1234\n" {
		t.Fatalf("escaped project was modified: %v", err)
	}
}

func TestDNSRejectsMalformedNames(t *testing.T) {
	for _, data := range [][]byte{{0xc0, 0}, {0xc0, 2, 0xc0, 0}, {64, 0}, {3, 'a'}, {1, 'a'}} {
		if _, end := readDNSName(data, 0); end != -1 {
			t.Fatalf("accepted malformed name: %v", data)
		}
	}
	if name, end := readDNSName([]byte{1, 'a', 4, 't', 'e', 's', 't', 0}, 0); name != "a.test." || end != 8 {
		t.Fatalf("valid name rejected: %q %d", name, end)
	}
}

func TestProxyRejectsUnconfiguredHosts(t *testing.T) {
	s := newServer(Config{})
	s.routes["demo"] = 3000
	for _, host := range []string{"example.com", "unknown.test", "demo.test.evil"} {
		req := httptest.NewRequest("GET", "http://"+host+"/", nil)
		res := httptest.NewRecorder()
		s.proxyHandler().ServeHTTP(res, req)
		if res.Code != 404 {
			t.Fatalf("host %q returned %d", host, res.Code)
		}
	}
}

func FuzzDNSName(f *testing.F) {
	f.Add([]byte{0xc0, 0})
	f.Add([]byte{1, 'a', 0})
	f.Fuzz(func(t *testing.T, data []byte) { readDNSName(data, 0) })
}

func TestPinnedPortsCannotCollideWithSavedAssignments(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		if err := os.MkdirAll(filepath.Join(root, name, "config"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name, "config", "application.rb"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "beta", "Procfile.dev"), []byte("web: rails s -p 4000"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{ProjectsDir: root, PortmapFile: filepath.Join(root, "portmap")}
	savePortmap(cfg.PortmapFile, []PortMapping{{App: "alpha", Port: 4000}, {App: "beta", Port: 4001}})
	mappings := buildPortmap(cfg)
	if len(mappings) != 2 || mappings[0].Port == mappings[1].Port {
		t.Fatalf("conflicting assignments: %v", mappings)
	}
	if mappings[1].Port != 4000 {
		t.Fatalf("pinned port was lost: %v", mappings)
	}
}

func TestPIDRecordMustMatchTheCurrentProcessIdentity(t *testing.T) {
	file := filepath.Join(t.TempDir(), "pid")
	writePid(file, os.Getpid())
	if readPid(file) != os.Getpid() {
		t.Fatal("valid process identity rejected")
	}
	if err := os.WriteFile(file, []byte(`{"pid":1,"identity":"forged"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if readPid(file) != 0 {
		t.Fatal("accepted a forged process identity")
	}
}

func TestProxyForwardsOnlyToTheConfiguredLoopbackPort(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("backend")) }))
	defer backend.Close()
	parsed, _ := url.Parse(backend.URL)
	_, portString, _ := net.SplitHostPort(parsed.Host)
	port, _ := strconv.Atoi(portString)
	s := newServer(Config{})
	s.routes["demo"] = port
	request := httptest.NewRequest("GET", "http://demo.test/", nil)
	response := httptest.NewRecorder()
	s.proxyHandler().ServeHTTP(response, request)
	if response.Code != 200 || response.Body.String() != "backend" {
		t.Fatalf("unexpected proxy response: %d %s", response.Code, response.Body.String())
	}
}
