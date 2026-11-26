//go:build wasip1

// The exports Girvi calls. Everything else is in handler.go, which has no
// WebAssembly in it, so it is tested with plain go test.
package main

import "github.com/anant-barlota/girvi-sdk/plugin"

// on_event is called for each event in plugin.json "events", and for Send test.
//
//go:wasmexport on_event
func onEvent() { plugin.Run(handle) }

// on_wake is called for each reminder the plugin scheduled with WakeAt.
//
//go:wasmexport on_wake
func onWake() { plugin.Run(handle) }

func main() {}
