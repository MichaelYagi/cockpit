//go:build linux

// Package linux contains Cockpit's Linux collectors.
//
// Collectors read through the roots in collector.Env (ProcRoot, SysRoot,
// EtcRoot, DevRoot) so they can be tested against fixture trees. External
// commands (smartctl) and syscalls (statfs) are injectable for the same
// reason. Parsers are pure functions in parse.go.
package linux
