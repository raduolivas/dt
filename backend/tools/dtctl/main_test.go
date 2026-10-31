package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBrandingFromFlags(t *testing.T) {
	if b, err := (tenantSettings{}).branding(); b != nil || err != nil {
		t.Fatalf("no flags: %v, %v", b, err)
	}
	p := filepath.Join(t.TempDir(), "logo.png")
	if err := os.WriteFile(p, []byte("logo-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := tenantSettings{brandName: "Acme", brandColor: "#112233", brandTheme: "dark", brandLogo: p}.branding()
	if err != nil {
		t.Fatal(err)
	}
	if b.GetDisplayName() != "Acme" || b.GetPrimaryColor() != "#112233" || b.GetTheme() != "dark" ||
		string(b.GetLogo()) != "logo-bytes" || b.GetLogoContentType() != "" {
		t.Fatalf("branding = %v", b)
	}
	if _, err := (tenantSettings{brandLogo: filepath.Join(t.TempDir(), "missing")}).branding(); err == nil {
		t.Fatal("missing logo file: want an error")
	}
}
