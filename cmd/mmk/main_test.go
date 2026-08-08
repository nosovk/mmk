package main

import "testing"

func TestApplicationIdentity(t *testing.T) {
	got := applicationIdentity()

	if got.executable != "mmk" {
		t.Errorf("executable = %q, want %q", got.executable, "mmk")
	}
	if got.configDirectory != "mmk" {
		t.Errorf("config directory = %q, want %q", got.configDirectory, "mmk")
	}
	if got.displayName != "mmk" {
		t.Errorf("display name = %q, want %q", got.displayName, "mmk")
	}
}
