package clix

import (
	"strings"
	"testing"
)

func TestParseOption(t *testing.T) {
	o, err := parseOption("Custom Product Page | faster, no new build")
	if err != nil || o.Label != "Custom Product Page" || o.Detail != "faster, no new build" {
		t.Fatalf("got %+v, %v", o, err)
	}
	o, err = parseOption("Ship with 1.0.2")
	if err != nil || o.Label != "Ship with 1.0.2" || o.Detail != "" {
		t.Fatalf("got %+v, %v", o, err)
	}
	if _, err := parseOption(""); err == nil {
		t.Error("empty option should be an error")
	}
}

func TestParseProgress(t *testing.T) {
	d, n, err := parseProgress("2/3")
	if err != nil || d != 2 || n != 3 {
		t.Fatalf("got %d/%d, %v", d, n, err)
	}
	for _, bad := range []string{"3/2", "x", "0/0", "-1/3"} {
		if _, _, err := parseProgress(bad); err == nil {
			t.Errorf("%q should be an error", bad)
		}
	}
}

func TestAskWithoutATaskIsRefused(t *testing.T) {
	t.Setenv("DISPATCH_TASK_ID", "")
	_, err := taskRef("")
	if err == nil || !strings.Contains(err.Error(), "DISPATCH_TASK_ID") || !strings.Contains(err.Error(), "--task") {
		t.Fatalf("err = %v", err)
	}
	t.Setenv("DISPATCH_TASK_ID", "task_x")
	if ref, err := taskRef(""); err != nil || ref != "task_x" {
		t.Fatalf("ref = %q, %v", ref, err)
	}
	if ref, _ := taskRef("explicit"); ref != "explicit" {
		t.Fatalf("flag should win, got %q", ref)
	}
}
