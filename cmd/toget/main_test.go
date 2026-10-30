package main

import "testing"

func TestFormatAccountID(t *testing.T) {
	if got := formatAccountID("68173501840479"); got != "68173501-840479" {
		t.Fatalf("formatAccountID = %q", got)
	}
	if got := formatAccountID("short"); got != "short" {
		t.Fatalf("short ID was changed: %q", got)
	}
}

func TestValidateLoopback(t *testing.T) {
	valid := []string{"127.0.0.1:7718", "localhost:7718", "[::1]:7718"}
	for _, listen := range valid {
		if err := validateLoopback(listen); err != nil {
			t.Errorf("validateLoopback(%q) = %v", listen, err)
		}
	}
	invalid := []string{"0.0.0.0:7718", ":7718", "192.168.1.2:7718", "not-an-address"}
	for _, listen := range invalid {
		if err := validateLoopback(listen); err == nil {
			t.Errorf("validateLoopback(%q) succeeded", listen)
		}
	}
}
