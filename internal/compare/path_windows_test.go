//go:build windows

package compare

import "testing"

func TestOnlyOrdinaryDriveAbsoluteRootSyntaxIsAccepted(t *testing.T) {
	for _, path := range []string{`C:\`, `D:/project`, `z:\проект\src`, `C:/mixed\separators`} {
		if !validRootSyntax(path) {
			t.Errorf("ordinary drive path rejected: %q", path)
		}
	}
	for _, path := range []string{
		`\\server\share`, `//server/share`, `\/server/share`, `/\server/share`,
		`\??\C:\folder`, `\\?\C:\folder`, `\\.\C:\folder`,
		`\folder`, `/folder`, `C:folder`, `1:\folder`, "C:\\a\x00b", "",
	} {
		if validRootPath(path) {
			t.Errorf("nonlocal/device/relative path accepted: %q", path)
		}
	}
}
