package main

// Version is the current application version.
//
// It is overridden at build time in CI/releases via:
//
//	-ldflags "-X main.Version=<version>"
var Version = "dev"
