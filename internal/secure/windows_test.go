//go:build windows

package secure

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestCurrentUserDPAPIIdentityAndStateRoundtrip(t *testing.T) {
	identity := identityForTest(t)
	protected, err := identity.Protect()
	if err != nil {
		t.Fatal("real CurrentUser DPAPI failed:", err)
	}
	if bytes.Contains(protected, []byte(identity.encryption.String())) || bytes.Contains(protected, identity.signing.Seed()) {
		t.Fatal("protected blob contains plaintext private material")
	}
	restored, err := Restore(protected)
	if err != nil || restored.Public() != identity.Public() {
		t.Fatal("identity restore did not preserve public identity", err)
	}
	payload := []byte("synthetic profile bytes; no keys are stored on disk")
	state, err := ProtectData(payload)
	if err != nil || bytes.Contains(state, payload) {
		t.Fatal("state protection failed", err)
	}
	decoded, err := UnprotectData(state)
	if err != nil || !bytes.Equal(decoded, payload) {
		t.Fatal("state unprotection failed", err)
	}
	clear(decoded)
	corrupted := append([]byte(nil), protected...)
	corrupted[len(corrupted)-1] ^= 1
	for _, invalid := range [][]byte{nil, protected[:len(protected)/2], corrupted} {
		if restored, err := Restore(invalid); err == nil || restored != nil {
			t.Fatal("invalid protected identity accepted")
		}
	}
	if restored, err := Restore(state); err == nil || restored != nil {
		t.Fatal("generic state parsed as private identity")
	}
	if _, err := Restore(make([]byte, MaxIdentityProtectedBytes+1)); !errors.Is(err, ErrSize) {
		t.Fatal("oversized identity blob reached DPAPI")
	}
}

func TestOwnerProcessHelper(t *testing.T) {
	if os.Getenv("FIGMOBIL_OWNER_TEST_CHILD") != "1" {
		return
	}
	if release, err := AcquireOwner(); !errors.Is(err, ErrOwnerExists) || release != nil {
		if release != nil {
			release()
		}
		t.Fatal("second process acquired device owner", err)
	}
}

func TestDeviceOwnerExcludesSecondProcessAndReleases(t *testing.T) {
	release, err := AcquireOwner()
	if err != nil {
		t.Fatal("device owner unavailable; close any running figmobil owner before this test:", err)
	}
	t.Cleanup(release)
	if otherRelease, err := AcquireOwner(); !errors.Is(err, ErrOwnerExists) || otherRelease != nil {
		if otherRelease != nil {
			otherRelease()
		}
		t.Fatal("second in-process owner accepted", err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestOwnerProcessHelper$", "-test.count=1")
	for _, value := range os.Environ() {
		if len(value) < len("FIGMOBIL_OWNER_TEST_CHILD=") || value[:len("FIGMOBIL_OWNER_TEST_CHILD=")] != "FIGMOBIL_OWNER_TEST_CHILD=" {
			child.Env = append(child.Env, value)
		}
	}
	child.Env = append(child.Env, "FIGMOBIL_OWNER_TEST_CHILD=1")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("synthetic child owner test failed: %v %s", err, output)
	}
	release()
	release()
	nextRelease, err := AcquireOwner()
	if err != nil {
		t.Fatal("owner lease was not released", err)
	}
	nextRelease()
}
