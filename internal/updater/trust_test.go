package updater

// Where an update is downloaded from is not the answering service's decision.
// The service names the build and the checksum it is verified against, so a
// checksum alone only proves the download matches what that answer asked for.
// These tests pin the one thing that makes the answer safe to act on.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTrustedDownload(t *testing.T) {
	good := []string{
		"https://github.com/XProject25/Storix/releases/download/v1.4.1/storix_1.4.1_linux_amd64.tar.gz",
		"https://objects.githubusercontent.com/anything",
		"https://api.github.com/repos/XProject25/Storix/releases/assets/1",
		"https://GitHub.com/XProject25/Storix/releases/download/v1/x.tar.gz",
	}
	for _, u := range good {
		if !trustedDownload(u) {
			t.Errorf("%s should be trusted", u)
		}
	}
	bad := []string{
		"",
		"http://github.com/XProject25/Storix/releases/download/v1/x.tar.gz", // not https
		"https://updates.xproject.live/evil.tar.gz",                         // the answering service itself
		"https://github.com.example.net/x.tar.gz",                           // lookalike
		"https://evil.net/github.com/x.tar.gz",
		"https://raw.githubusercontent.com/x",
		"ftp://github.com/x",
		"://nonsense",
	}
	for _, u := range bad {
		if trustedDownload(u) {
			t.Errorf("%s must not be trusted", u)
		}
	}
}

// TestApplyRefusesAnUntrustedAsset is the part that matters: the refusal has
// to happen in Apply, before anything is fetched or written.
func TestApplyRefusesAnUntrustedAsset(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "storix")
	if err := os.WriteFile(binary, []byte("current"), 0o755); err != nil {
		t.Fatal(err)
	}
	u := New(Options{Current: "1.4.0", BinaryPath: binary})

	rel := &Release{
		Available: true,
		Version:   "9.9.9",
		Asset:     "storix_9.9.9_linux_amd64.tar.gz",
		AssetURL:  "https://updates.xproject.live/storix_9.9.9_linux_amd64.tar.gz",
		Checksum:  "https://updates.xproject.live/checksums.txt",
	}
	if err := u.Apply(context.Background(), rel, nil); !errors.Is(err, ErrUntrustedAsset) {
		t.Fatalf("Apply returned %v, want it to refuse an update hosted anywhere but the project", err)
	}

	// A trusted build with an untrusted checksum list is refused too: the
	// checksum is what decides whether the download is the right one.
	rel.AssetURL = "https://github.com/XProject25/Storix/releases/download/v9.9.9/storix_9.9.9_linux_amd64.tar.gz"
	if err := u.Apply(context.Background(), rel, nil); !errors.Is(err, ErrUntrustedAsset) {
		t.Fatalf("Apply returned %v, want it to refuse a checksum list from somewhere else", err)
	}

	// The binary must be exactly as it was.
	got, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "current" {
		t.Fatal("the binary was touched by a refused update")
	}
}
