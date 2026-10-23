package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"figmobil/internal/secure"
	"figmobil/internal/transferlimit"
)

type auditGate struct {
	sizes []int
	deny  bool
}

func (g *auditGate) TryAcquire(data []byte) (transferlimit.Decision, error) {
	g.sizes = append(g.sizes, len(data))
	return transferlimit.Decision{Allowed: !g.deny, RetryAfter: time.Second}, nil
}

type device struct {
	m             *Manager
	root, dir, id string
	clock         time.Time
	gate          *auditGate
}

func newDevice(t *testing.T, files map[string][]byte) *device {
	t.Helper()
	d := &device{root: t.TempDir(), dir: t.TempDir(), clock: time.Unix(1_000_000, 0), gate: &auditGate{}}
	for path, data := range files {
		writeFixture(t, d.root, path, data)
	}
	var err error
	d.m, err = newManager(d.dir, d.gate)
	if err != nil {
		t.Fatal(err)
	}
	d.m.now = func() time.Time { return d.clock }
	return d
}
func writeFixture(t *testing.T, root, path string, data []byte) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func (d *device) action(r Request) (Response, error) {
	d.clock = d.clock.Add(3 * time.Second)
	if r.Action != "create" && r.Action != "join" && r.ProfileID == "" {
		r.ProfileID = d.id
	}
	var response Response
	err := d.m.Do(context.Background(), r, func(result Response) error { response = result; return nil })
	if response.Profile != nil {
		d.id = response.Profile.ID
	}
	return response, err
}
func (d *device) must(t *testing.T, r Request) Response {
	t.Helper()
	response, err := d.action(r)
	if err != nil {
		t.Fatalf("%s: %v", r.Action, err)
	}
	return response
}
func transfer(t *testing.T, from, to *device) {
	t.Helper()
	state := from.must(t, Request{Action: "state"})
	if state.Profile.Outgoing == nil {
		t.Fatal("no outgoing")
	}
	for state.Profile.Outgoing.Next < state.Profile.Outgoing.Parts {
		part := from.must(t, Request{Action: "next"})
		if part.Text == "" {
			t.Fatal("no gated text")
		}
		to.must(t, Request{Action: "receive", Text: part.Text})
		state = from.must(t, Request{Action: "state"})
	}
}
func pair(t *testing.T, a, b *device) {
	t.Helper()
	a.must(t, Request{Action: "create", Name: "test project", DeviceName: "A", Root: a.root})
	invite := a.must(t, Request{Action: "next"})
	b.must(t, Request{Action: "join", Name: "test project", DeviceName: "B", Root: b.root, Text: invite.Text})
	transfer(t, b, a)
	va := a.must(t, Request{Action: "state"})
	vb := b.must(t, Request{Action: "state"})
	if va.Profile.VerificationCode != vb.Profile.VerificationCode || len(va.Profile.VerificationCode) < 32 {
		t.Fatal("transcript mismatch")
	}
	a.must(t, Request{Action: "confirm", Code: va.Profile.VerificationCode})
	b.must(t, Request{Action: "confirm", Code: vb.Profile.VerificationCode})
	transfer(t, a, b)
	transfer(t, b, a)
	if !a.must(t, Request{Action: "state"}).Profile.PeerConfirmed || !b.must(t, Request{Action: "state"}).Profile.PeerConfirmed {
		t.Fatal("peer confirmation missing")
	}
}
func applyIncoming(t *testing.T, d *device) Response {
	t.Helper()
	preview := d.must(t, Request{Action: "preview"})
	if preview.Plan == nil || !preview.Plan.CanApply {
		t.Fatalf("plan blocked: %+v", preview.Plan)
	}
	return d.must(t, Request{Action: "apply", PlanID: preview.Plan.ID})
}
func establish(t *testing.T, a, b *device) {
	t.Helper()
	a.must(t, Request{Action: "export"})
	transfer(t, a, b)
	applyIncoming(t, b)
	transfer(t, b, a)
	if a.m.profiles[a.id].Base == nil || a.m.profiles[a.id].Base.ID != b.m.profiles[b.id].Base.ID {
		t.Fatal("base not agreed")
	}
}

func TestManualRoundTripAndProtectedRestart(t *testing.T) {
	files := map[string][]byte{"main.txt": []byte("one\r\ntwo\r\n"), "keep.bin": {0, 255, 1, 2}}
	a, b := newDevice(t, files), newDevice(t, files)
	pair(t, a, b)
	establish(t, a, b)
	before, err := os.Stat(filepath.Join(b.root, "keep.bin"))
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, a.root, "main.txt", []byte("ONE\r\ntwo\r\n"))
	a.must(t, Request{Action: "export"})
	// Reload restores a prepared encrypted outbox without issuing, scanning or applying.
	loaded, err := newManager(a.dir, a.gate)
	if err != nil {
		t.Fatal(err)
	}
	loaded.now = func() time.Time { return a.clock }
	a.m = loaded
	state := a.must(t, Request{Action: "state"})
	if state.Text != "" || state.Profile.Outgoing.Next != 0 {
		t.Fatal("automatic issuance after restart")
	}
	transfer(t, a, b)
	applyIncoming(t, b)
	transfer(t, b, a)
	writeFixture(t, b.root, "main.txt", []byte("ONE\r\ncolleague\r\ntwo\r\n"))
	b.must(t, Request{Action: "export"})
	transfer(t, b, a)
	applyIncoming(t, a)
	transfer(t, a, b)
	for _, d := range []*device{a, b} {
		data, err := os.ReadFile(filepath.Join(d.root, "main.txt"))
		if err != nil || string(data) != "ONE\r\ncolleague\r\ntwo\r\n" {
			t.Fatal("bytes changed")
		}
		for _, size := range d.gate.sizes {
			if size <= 0 || size > 250000 {
				t.Fatal("gate bypass")
			}
		}
		saved, err := os.ReadFile(filepath.Join(d.dir, d.id+".fmstate"))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(saved, []byte("colleague")) || bytes.Contains(saved, []byte("AGE-SECRET-KEY")) {
			t.Fatal("plaintext state")
		}
	}
	after, err := os.Stat(filepath.Join(b.root, "keep.bin"))
	if err != nil || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("untouched file rewritten")
	}
}

func TestLocalChangeReturnsExactMergedResultWithoutFalseACK(t *testing.T) {
	files := map[string][]byte{"main.txt": []byte("base\n"), "local.txt": []byte("base\n")}
	a, b := newDevice(t, files), newDevice(t, files)
	pair(t, a, b)
	establish(t, a, b)
	oldBase := a.m.profiles[a.id].Base.ID
	writeFixture(t, b.root, "local.txt", []byte("colleague\n"))
	writeFixture(t, a.root, "main.txt", []byte("development\n"))
	a.must(t, Request{Action: "export"})
	transfer(t, a, b)
	result := applyIncoming(t, b)
	if result.Profile.BaseID != oldBase || result.Profile.Incoming == nil || !result.Profile.Incoming.Ready || result.Profile.Outgoing.Kind == "ack" && result.Profile.Outgoing.Next == 0 {
		t.Fatal("false full snapshot ACK")
	}
	b.must(t, Request{Action: "resolve"})
	transfer(t, b, a)
	applyIncoming(t, a)
	transfer(t, a, b)
	for _, d := range []*device{a, b} {
		main, _ := os.ReadFile(filepath.Join(d.root, "main.txt"))
		local, _ := os.ReadFile(filepath.Join(d.root, "local.txt"))
		if string(main) != "development\n" || string(local) != "colleague\n" {
			t.Fatal("merged files lost")
		}
	}
	if a.m.profiles[a.id].Base.ID != b.m.profiles[b.id].Base.ID {
		t.Fatal("merged result base mismatch")
	}
}

func TestInitialDifferenceAndStalePreviewDoNotOverwrite(t *testing.T) {
	a := newDevice(t, map[string][]byte{"main.txt": []byte("A")})
	b := newDevice(t, map[string][]byte{"main.txt": []byte("B")})
	pair(t, a, b)
	a.must(t, Request{Action: "export"})
	transfer(t, a, b)
	preview := b.must(t, Request{Action: "preview"})
	if preview.Plan.CanApply {
		t.Fatal("unknown base allowed overwrite")
	}
	if _, err := b.action(Request{Action: "apply", PlanID: preview.Plan.ID}); err == nil {
		t.Fatal("blocked plan applied")
	}
	data, _ := os.ReadFile(filepath.Join(b.root, "main.txt"))
	if string(data) != "B" {
		t.Fatal("initial local edit lost")
	}
	// The user explicitly returns the chosen manual result; A reviews it against its own outgoing snapshot.
	writeFixture(t, b.root, "main.txt", []byte("manual merge"))
	b.must(t, Request{Action: "resolve"})
	transfer(t, b, a)
	applyIncoming(t, a)
	transfer(t, a, b)
	writeFixture(t, a.root, "main.txt", []byte("incoming"))
	a.must(t, Request{Action: "export"})
	transfer(t, a, b)
	preview = b.must(t, Request{Action: "preview"})
	writeFixture(t, b.root, "main.txt", []byte("edited after preview"))
	if _, err := b.action(Request{Action: "apply", PlanID: preview.Plan.ID}); err == nil {
		t.Fatal("stale plan applied")
	}
	data, _ = os.ReadFile(filepath.Join(b.root, "main.txt"))
	if string(data) != "edited after preview" {
		t.Fatal("stale overwrite")
	}
}

func TestRateGateAndDelayedHandoffHaveNoOutputBypass(t *testing.T) {
	d := newDevice(t, nil)
	d.must(t, Request{Action: "create", Name: "project", DeviceName: "A", Root: d.root})
	d.gate.deny = true
	r, err := d.action(Request{Action: "next"})
	if err == nil || r.Text != "" || d.m.profiles[d.id].Outgoing.Next != 0 {
		t.Fatal("denied issuance leaked")
	}
	d.gate.deny = false
	err = d.m.Do(context.Background(), Request{Action: "next", ProfileID: d.id}, func(r Response) error {
		if r.Text == "" {
			t.Fatal("missing text")
		}
		d.clock = d.clock.Add(5 * time.Minute)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var emitted bool
	err = d.m.Do(context.Background(), Request{Action: "repeat", ProfileID: d.id, PartNumber: 1}, func(r Response) error { emitted = true; return nil })
	var problem *Error
	if !errors.As(err, &problem) || problem.Code != "RATE_LIMIT" || emitted {
		t.Fatal("delayed handoff allowed burst")
	}
	state := d.must(t, Request{Action: "state"})
	if state.Text != "" {
		t.Fatal("state exposed frame")
	}
	d.must(t, Request{Action: "repeat", PartNumber: 1})
	if len(d.gate.sizes) != 3 {
		t.Fatalf("next/repeat did not share gate: %d", len(d.gate.sizes))
	}
}

func TestFrameAssemblyBoundsDuplicatesAndMixing(t *testing.T) {
	data := bytes.Repeat([]byte{42}, rawPartBytes+3)
	first, _ := frameAt(data, 1)
	last, _ := frameAt(data, 2)
	var a assembly
	if complete, err := a.add(last); err != nil || complete != nil {
		t.Fatal("out of order")
	}
	if _, err := a.add(last); err != nil || a.Bytes != 3 {
		t.Fatal("duplicate counted")
	}
	other, _ := frameAt([]byte("other"), 1)
	if _, err := a.add(other); err == nil {
		t.Fatal("mixed accepted")
	}
	complete, err := a.add(first)
	if err != nil || !bytes.Equal(complete, data) {
		t.Fatal("assembly")
	}
	if _, _, _, _, err := parseFrame(first[:len(first)-1] + "A"); err == nil {
		t.Fatal("corruption accepted")
	}
	if _, _, _, _, err := parseFrame(strings.Repeat("x", 250001)); err == nil {
		t.Fatal("oversized accepted")
	}
}

func TestUnconfirmedAndWrongSenderRejected(t *testing.T) {
	a, b := newDevice(t, nil), newDevice(t, nil)
	a.must(t, Request{Action: "create", Name: "project", DeviceName: "A", Root: a.root})
	inv := a.must(t, Request{Action: "next"})
	b.must(t, Request{Action: "join", Name: "project", DeviceName: "B", Root: b.root, Text: inv.Text})
	transfer(t, b, a)
	if _, err := a.action(Request{Action: "export"}); err == nil {
		t.Fatal("unconfirmed export")
	}
	if _, err := a.action(Request{Action: "confirm", Code: "0000"}); err == nil {
		t.Fatal("bad code")
	}
	state := a.must(t, Request{Action: "state"})
	a.must(t, Request{Action: "confirm", Code: state.Profile.VerificationCode})
	state = b.must(t, Request{Action: "state"})
	b.must(t, Request{Action: "confirm", Code: state.Profile.VerificationCode})
	transfer(t, a, b)
	transfer(t, b, a)
	attacker, _ := secure.Generate()
	peer := b.m.profiles[b.id]
	own, _ := secure.Restore(peer.Private)
	payload, _ := json.Marshal(packet{Version: 1, Type: "confirm", ID: strings.Repeat("a", 32), ProjectID: peer.ID, InviteID: peer.InviteID, Transcript: verificationCode(peer, own)})
	sealed, _ := attacker.Seal(own.Public(), payload)
	data, _ := json.Marshal(wire{Version: 1, Kind: "sealed", ProjectID: peer.ID, InviteID: peer.InviteID, Body: sealed})
	frame, _ := frameAt(data, 1)
	if _, err := b.action(Request{Action: "receive", Text: frame}); err == nil {
		t.Fatal("wrong trusted sender accepted")
	}
}

func TestInvalidFrameThenSuccessfulIssuanceDoesNotPoisonRestart(t *testing.T) {
	d := newDevice(t, nil)
	d.must(t, Request{Action: "create", Name: "project", DeviceName: "A", Root: d.root})
	if _, err := d.action(Request{Action: "receive", Text: "garbage"}); err == nil {
		t.Fatal("invalid frame accepted")
	}
	if d.m.profiles[d.id].Inbox != nil {
		t.Fatal("invalid empty inbox retained")
	}
	d.must(t, Request{Action: "next"})
	if _, err := newManager(d.dir, &auditGate{}); err != nil {
		t.Fatal("invalid frame poisoned restart:", err)
	}
}

func TestDiscardAllowsExactDataReimportAndKeepsOutstandingOutbox(t *testing.T) {
	a, b := newDevice(t, map[string][]byte{"file": []byte("base")}), newDevice(t, map[string][]byte{"file": []byte("base")})
	pair(t, a, b)
	establish(t, a, b)
	writeFixture(t, a.root, "file", []byte("incoming"))
	a.must(t, Request{Action: "export"})
	transfer(t, a, b)
	b.must(t, Request{Action: "discard"})
	part := a.must(t, Request{Action: "repeat", PartNumber: 1})
	b.must(t, Request{Action: "receive", Text: part.Text})
	preview := b.must(t, Request{Action: "preview"})
	if !preview.Plan.CanApply {
		t.Fatal("discard lost retry")
	}
	// A is still awaiting ACK, but may discard an unrelated incomplete receive.
	a.must(t, Request{Action: "discard"})
	if !a.m.profiles[a.id].Outgoing.AwaitingAck {
		t.Fatal("discard reset outgoing version")
	}
	applyIncoming(t, b)
	transfer(t, b, a)
}

func TestRejectedRetainedBudgetAndFailedSaveDoNotAdvanceMemory(t *testing.T) {
	a, b := newDevice(t, nil), newDevice(t, nil)
	pair(t, a, b)
	establish(t, a, b)
	a.m.retainedLimit = 1
	if _, err := a.action(Request{Action: "export"}); err == nil {
		t.Fatal("budget accepted")
	}
	if a.m.profiles[a.id].Outgoing != nil {
		t.Fatal("rejected outbox retained")
	}
	a.m.retainedLimit = maxRetainedBytes
	if _, err := newManager(a.dir, &auditGate{}); err != nil {
		t.Fatal("budget poisoned persisted state:", err)
	}
	a.must(t, Request{Action: "export"})
	previousNext := a.m.profiles[a.id].Outgoing.Next
	priorDir := a.m.dir
	a.m.dir = filepath.Join(t.TempDir(), "missing")
	if response, err := a.action(Request{Action: "next"}); err == nil || response.Text != "" {
		t.Fatal("state failure leaked text")
	}
	a.m.dir = priorDir
	if a.m.profiles[a.id].Outgoing.Next != previousNext {
		t.Fatal("failed save advanced issuance")
	}
}

func TestProtocolJSONRejectsDuplicateDeepAndLargeCollections(t *testing.T) {
	for _, data := range []string{`{"version":1,"version":1}`, strings.Repeat("[", 34) + "0" + strings.Repeat("]", 34), "[" + strings.Repeat("0,", 5000) + "0]"} {
		var value any
		if strictJSON([]byte(data), &value) == nil {
			t.Fatal("invalid JSON shape accepted")
		}
	}
	var value map[string]any
	if err := strictJSON([]byte(`{"a":[1,{"b":"test"}]}`), &value); err != nil {
		t.Fatal(err)
	}
}

func TestAllProfilesShareHandoffCooldownAndCloseRejectsLateHandlers(t *testing.T) {
	d := newDevice(t, nil)
	first := d.must(t, Request{Action: "create", Name: "first", DeviceName: "A", Root: d.root}).Profile.ID
	second := d.must(t, Request{Action: "create", Name: "second", DeviceName: "A", Root: d.root}).Profile.ID
	d.must(t, Request{Action: "next", ProfileID: first})
	err := d.m.Do(context.Background(), Request{Action: "next", ProfileID: second}, func(Response) error { t.Fatal("second profile bypassed cooldown"); return nil })
	var problem *Error
	if !errors.As(err, &problem) || problem.Code != "RATE_LIMIT" {
		t.Fatal("per-profile quota bypass")
	}
	d.m.Close()
	err = d.m.Do(context.Background(), Request{Action: "state"}, func(Response) error { t.Fatal("closed manager emitted response"); return nil })
	if !errors.As(err, &problem) || problem.Code != "CHANNEL_CLOSED" {
		t.Fatal("late handler accepted after owner drain")
	}
}

func TestEncryptedChannelPreservesOrdinaryGitDiffAndStaging(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git not installed")
	}
	command := func(root string, args ...string) string {
		t.Helper()
		prefix := []string{"--no-pager", "-C", root, "-c", "core.hooksPath=NUL", "-c", "core.fsmonitor=false", "-c", "commit.gpgsign=false", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}
		cmd := exec.Command(git, append(prefix, args...)...)
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(strings.ToUpper(entry), "GIT_") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=NUL", "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("synthetic git: %v %s", err, out)
		}
		return string(out)
	}
	files := map[string][]byte{"main.txt": []byte("one\r\ntwo\r\n"), "stage.txt": []byte("base\n"), "local.txt": []byte("base\n"), "utf16.bin": {255, 254, 65, 0, 13, 0, 10, 0}}
	a, b := newDevice(t, files), newDevice(t, files)
	for _, d := range []*device{a, b} {
		command(d.root, "init", "--quiet")
		command(d.root, "config", "core.autocrlf", "false")
		command(d.root, "config", "core.eol", "lf")
		command(d.root, "add", "--all")
		command(d.root, "commit", "--quiet", "-m", "synthetic baseline")
	}
	pair(t, a, b)
	establish(t, a, b)
	// Re-exporting the same confirmed snapshot produces no working-tree diff.
	a.must(t, Request{Action: "export"})
	transfer(t, a, b)
	applyIncoming(t, b)
	transfer(t, b, a)
	for _, d := range []*device{a, b} {
		if command(d.root, "diff", "--no-ext-diff", "--no-textconv", "--numstat") != "" {
			t.Fatal("no-change transfer produced diff")
		}
	}
	writeFixture(t, b.root, "stage.txt", []byte("staged colleague\n"))
	command(b.root, "add", "--", "stage.txt")
	writeFixture(t, b.root, "local.txt", []byte("unstaged colleague\n"))
	staged := command(b.root, "diff", "--cached", "--no-ext-diff", "--no-textconv")
	indexBefore, _ := os.ReadFile(filepath.Join(b.root, ".git", "index"))
	keepBefore, _ := os.Stat(filepath.Join(b.root, "utf16.bin"))
	writeFixture(t, a.root, "main.txt", []byte("ONE\r\ntwo\r\n"))
	a.must(t, Request{Action: "export"})
	transfer(t, a, b)
	applyIncoming(t, b)
	indexAfter, _ := os.ReadFile(filepath.Join(b.root, ".git", "index"))
	if !bytes.Equal(indexBefore, indexAfter) || staged != command(b.root, "diff", "--cached", "--no-ext-diff", "--no-textconv") {
		t.Fatal("channel changed staging/index")
	}
	if stat := command(b.root, "diff", "--no-ext-diff", "--no-textconv", "--numstat", "--", "main.txt"); stat != "1\t1\tmain.txt\n" {
		t.Fatalf("false line changes: %q", stat)
	}
	keepAfter, _ := os.Stat(filepath.Join(b.root, "utf16.bin"))
	if !keepBefore.ModTime().Equal(keepAfter.ModTime()) {
		t.Fatal("untouched UTF16 file rewritten")
	}
	b.must(t, Request{Action: "resolve"})
	transfer(t, b, a)
	applyIncoming(t, a)
	transfer(t, a, b)
	if a.m.profiles[a.id].Base.ID != b.m.profiles[b.id].Base.ID {
		t.Fatal("final negotiated result mismatch")
	}
}
