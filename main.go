package main

import (
	"fmt"
	"os"
)

const version = "0.3.0"

func main() {
	if len(os.Args) < 2 {
		cmdSync()
		return
	}

	switch os.Args[1] {
	case "sync":
		cmdSync()
	case "list", "ls":
		cmdList()
	case "up":
		cmdUp()
	case "down":
		cmdDown()
	case "setup":
		cmdSetup()
	case "clean":
		cmdClean()
	case "version", "--version":
		fmt.Println("dot-test", version)
	case "help", "--help", "-h":
		printHelp()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printHelp()
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Print(`dot-test — named .test URLs for local dev servers

Usage:
  dot-test [sync]    Discover projects, assign ports, update .env files
  dot-test list      Show current port mappings
  dot-test up        Start DNS + reverse proxy daemon
  dot-test down      Stop daemon
  dot-test setup     One-time OS configuration (creates /etc/resolver/test)
  dot-test clean     Remove all config and stop daemon

Environment:
  DOT_TEST_DIR       Projects directory (default: current directory)
  DOT_TEST_PORT      Proxy listen port (default: 80)
  DOT_TEST_DNS_PORT  DNS listen port (default: 15353)

Quickstart:
  brew tap zarpay/tap && brew install dot-test
  dot-test setup                   # one-time, needs sudo
  cd ~/projects && dot-test sync   # discover apps, assign ports
  brew services start dot-test     # start daemon (survives reboots)
`)
}
