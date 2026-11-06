package agent

import (
	"strings"
	"testing"
)

func TestPairingCodeRoundTrip(t *testing.T) {
	dir := t.TempDir()
	code, err := NewPairingCode(dir)
	if err != nil {
		t.Fatal(err)
	}
	fp, tok, err := ParsePairingCode(code)
	if err != nil || len(fp) != 32 || len(tok) != 16 {
		t.Fatalf("parse: %v %d %d", err, len(fp), len(tok))
	}
	if _, _, err := ParsePairingCode(strings.Replace(code, "slp1", "slp2", 1)); err == nil {
		t.Fatal("expected version error")
	}
}
