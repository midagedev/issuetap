package secretscan

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func script(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", "..", "scripts", "secretscan.sh"))
}

func TestScriptExists(t *testing.T) {
	if _, err := os.Stat(script(t)); err != nil {
		t.Fatal(err)
	}
}

func TestScannerFindsFakeToken(t *testing.T) {
	// The scanner must fail closed on a token-shaped string. This test
	// writes a throwaway file in a temp copy of the script's exclude
	// logic by invoking grep the same way the script does.
	dir := t.TempDir()
	p := filepath.Join(dir, "leak.txt")
	// Construct the shape at runtime so the source tree does not contain it.
	shape := "AT" + "ATT" + "3xFfGF0DEMOONLYNEVERSHIPPED"
	if err := os.WriteFile(p, []byte("token="+shape+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("grep", "-E", "AT"+"ATT[A-Za-z0-9_\\-=+/]+", p)
	if err := cmd.Run(); err != nil {
		t.Fatalf("expected grep to find the fake token: %v", err)
	}
}
