package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Changes made by the sender are source edits, not normalization by transport.
func TestIncomingRepresentationEditIsCopiedAsExactBytes(t *testing.T) {
	cases := []struct{ name, before, after string }{
		{"empty-to-first-line", "", "first line\n"},
		{"explicit-bom", "one\n", "\xef\xbb\xbfone\n"},
		{"utf16-edit", "\xff\xfeA\x00\r\x00\n\x00", "\xff\xfeB\x00\r\x00\n\x00"},
		{"explicit-eol-edit", "one\r\ntwo\r\n", "one\ntwo\n"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			base := fixture(t, map[string]string{"file": item.before, "keep": "unchanged"})
			local := fixture(t, map[string]string{"file": item.before, "keep": "unchanged"})
			incoming := fixture(t, map[string]string{"file": item.after, "keep": "unchanged"})
			plan, err := Plan(&base.Manifest, local, incoming, nil)
			if err != nil || !plan.CanApply {
				t.Fatalf("source edit blocked: %v %+v", err, plan)
			}
			before, err := os.Stat(filepath.Join(local.Root, "keep"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Apply(context.Background(), plan, incoming, t.TempDir()); err != nil {
				t.Fatal(err)
			}
			if got := contents(t, local.Root, "file"); got != item.after {
				t.Fatalf("source bytes transformed: %q", got)
			}
			after, err := os.Stat(filepath.Join(local.Root, "keep"))
			if err != nil || !before.ModTime().Equal(after.ModTime()) {
				t.Fatal("unchanged file rewritten")
			}
		})
	}
}
