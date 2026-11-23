package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"version"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "stockpot dev") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	if err := run(context.Background(), []string{"nope"}, &bytes.Buffer{}); err == nil {
		t.Fatal("want an error")
	}
}
