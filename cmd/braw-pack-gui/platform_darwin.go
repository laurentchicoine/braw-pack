//go:build darwin

package main

/*
// Wails' native file dialogs use UTType on current Xcode SDKs. Link its framework
// explicitly when using plain go build rather than the Wails build tool.
#cgo LDFLAGS: -framework UniformTypeIdentifiers
*/
import "C"
