package main

import (
	"path/filepath"
	"testing"

	"github.com/hetchyhq/hetchy/internal/bootstrap"
)

func TestInferIdentityPrefersGoModule(t *testing.T) {
	got := inferIdentity(&bootstrap.Hints{
		GoMod: &bootstrap.GoMod{Module: "github.com/acme/widget"},
	}, filepath.Join("tmp", "repo"))

	if got != "github.com/acme/widget" {
		t.Fatalf("inferIdentity() = %q, want module", got)
	}
}

func TestInferIdentityFallsBackToDirectoryName(t *testing.T) {
	got := inferIdentity(&bootstrap.Hints{}, filepath.Join("tmp", "repo-name"))

	if got != "repo-name" {
		t.Fatalf("inferIdentity() = %q, want directory base", got)
	}
}
