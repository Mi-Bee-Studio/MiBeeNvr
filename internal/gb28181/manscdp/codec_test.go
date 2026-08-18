package manscdp

import (
	"strings"
	"testing"
)

// TestSSRCDownload: the download variant carries the leading digit 2 (2022
// Annex C.2.4 extension) and otherwise follows the same composition rules.
func TestSSRCDownload(t *testing.T) {
	got := SSRCDownload("34020000002000000001", 7)
	if !strings.HasPrefix(got, "2") {
		t.Fatalf("download SSRC must start with 2, got %q", got)
	}
	if len(got) != 10 {
		t.Fatalf("SSRC must be 10 digits, got %q", got)
	}
	// Digits 2-6 come from server ID digits 4-8 ("20000"), tail is the seq.
	if got[1:6] != "20000" || got[6:] != "0007" {
		t.Fatalf("unexpected SSRC composition %q", got)
	}
}
