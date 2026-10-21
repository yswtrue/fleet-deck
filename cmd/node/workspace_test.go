package main

import "testing"

func TestResolveHubBase(t *testing.T) {
	t.Setenv("FLEETDECK_HUB_URL", "")
	t.Setenv("FLEETDECK_HUB_ADDR", "")

	got, err := resolveHubBase("http://hub.example.com:7710/", "")
	if err != nil || got != "http://hub.example.com:7710" {
		t.Fatalf("flag: got %q, err %v", got, err)
	}

	got, err = resolveHubBase("", "ws://127.0.0.1:7710/ws/node")
	if err != nil || got != "http://127.0.0.1:7710" {
		t.Fatalf("config ws: got %q, err %v", got, err)
	}

	got, err = resolveHubBase("", "wss://hub.example.com/ws/node")
	if err != nil || got != "https://hub.example.com" {
		t.Fatalf("config wss: got %q, err %v", got, err)
	}

	t.Setenv("FLEETDECK_HUB_ADDR", "10.0.0.5:7710")
	got, err = resolveHubBase("", "")
	if err != nil || got != "http://10.0.0.5:7710" {
		t.Fatalf("env addr: got %q, err %v", got, err)
	}

	if _, err := resolveHubBase("", ""); err != nil {
		t.Fatalf("unexpected error with env address: %v", err)
	}

	if _, err := resolveHubBase("ws://hub/ws/node", ""); err == nil {
		t.Fatal("expected an error for a non-http hub flag URL")
	}
}

func TestIsAbsolutePath(t *testing.T) {
	for _, p := range []string{"/srv/app", "/", `C:\code\app`, "d:/code/app"} {
		if !isAbsolutePath(p) {
			t.Fatalf("isAbsolutePath(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"", "srv/app", "./app", "C:app", "1:/x"} {
		if isAbsolutePath(p) {
			t.Fatalf("isAbsolutePath(%q) = true, want false", p)
		}
	}
}
