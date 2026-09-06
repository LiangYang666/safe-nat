// Command safenat is the CLI entrypoint of safe-nat:
// a NAT penetration tool whose exposed ports can be guarded by an
// IP-whitelist firewall, managed from a built-in web UI.
//
// Design reference: tasks/20260906-go-liangnat/design.md (LiangNat Go rewrite).
package main

import (
	"flag"
	"fmt"
	"os"
)

const version = "0.1.0" // M0: skeleton

const usageText = `safenat - secure NAT penetration (Go)

Usage:
  safenat server -c <server.yaml>   run the public server (cloud side)
  safenat client -c <client.yaml>   run the client (LAN side)
  safenat version                   print version

Global design (see docs):
  - one control TCP connection, data multiplexed over it with conn IDs
  - per-tunnel firewall flag, optional IP/CIDR whitelist enforced at accept
  - web management UI embedded in the server binary
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, usageText)
		return 2
	}
	switch args[0] {
	case "server":
		return runServer(args[1:])
	case "client":
		return runClient(args[1:])
	case "version", "-v", "--version":
		fmt.Printf("safenat %s\n", version)
		return 0
	case "help", "-h", "--help":
		fmt.Print(usageText)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", args[0], usageText)
		return 2
	}
}

// configPath parses "-c <path>" out of args, defaulting to def.
func configPath(args []string, def string) string {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cfg := fs.String("c", def, "config file path")
	_ = fs.Parse(args) // unknown flags are ignored at M0; strict parsing lands with M1
	return *cfg
}

func runServer(args []string) int {
	path := configPath(args, "config_server.yaml")
	fmt.Fprintf(os.Stderr, "safenat: server mode not implemented yet (M1) -c %s\n", path)
	return 1
}

func runClient(args []string) int {
	path := configPath(args, "config_client.yaml")
	fmt.Fprintf(os.Stderr, "safenat: client mode not implemented yet (M1) -c %s\n", path)
	return 1
}
