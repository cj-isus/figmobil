package channel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"figmobil/internal/compare"
	"figmobil/internal/secure"
	"figmobil/internal/transferlimit"
	"figmobil/internal/workspace"
)

type Request struct {
	Action     string   `json:"action"`
	ProfileID  string   `json:"profileId,omitempty"`
	Name       string   `json:"name,omitempty"`
	DeviceName string   `json:"deviceName,omitempty"`
	Root       string   `json:"root,omitempty"`
	Excludes   []string `json:"excludes,omitempty"`
	Text       string   `json:"text,omitempty"`
	Code       string   `json:"code,omitempty"`
	Deletes    []string `json:"deletes,omitempty"`
	PlanID     string   `json:"planId,omitempty"`
	PartNumber int      `json:"partNumber,omitempty"`
}
type Summary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type OutgoingView struct {
	Kind        string `json:"kind"`
	Parts       int    `json:"parts"`
	Next        int    `json:"next"`
	AwaitingAck bool   `json:"awaitingAck"`
}
type IncomingView struct {
	ID       string `json:"id"`
	Received int    `json:"received"`
	Total    int    `json:"total"`
	Ready    bool   `json:"ready"`
	Kind     string `json:"kind"`
}
type ProfileView struct {
	ID               string        `json:"id"`
	Name             string        `json:"name"`
	DeviceName       string        `json:"deviceName"`
	Root             string        `json:"root"`
	Excludes         []string      `json:"excludes"`
	Stage            string        `json:"stage"`
	VerificationCode string        `json:"verificationCode"`
	Confirmed        bool          `json:"confirmed"`
	PeerConfirmed    bool          `json:"peerConfirmed"`
	BaseID           string        `json:"baseId"`
	Outgoing         *OutgoingView `json:"outgoing,omitempty"`
	Incoming         *IncomingView `json:"incoming,omitempty"`
	NeedsRecovery    bool          `json:"needsRecovery"`
}
type PreviewItem struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Action string `json:"action"`
	Reason string `json:"reason"`
}
type Preview struct {
	ID       string        `json:"id"`
	Items    []PreviewItem `json:"items"`
	CanApply bool          `json:"canApply"`
}
type Response struct {
	Profiles []Summary    `json:"profiles"`
	Profile  *ProfileView `json:"profile,omitempty"`
	Message  string       `json:"message,omitempty"`
	Text     string       `json:"text,omitempty"`
	Plan     *Preview     `json:"plan,omitempty"`
}
type Error struct {
	Code         string `json:"code"`
	Message      string `json:"message"`
	RetryAfterMs int64  `json:"retryAfterMs,omitempty"`
}

func (e *Error) Error() string           { return e.Code }
func failure(code, message string) error { return &Error{Code: code, Message: message} }

type admission interface {
	TryAcquire([]byte) (transferlimit.Decision, error)
}

// Manager has one gate and mutex for every profile. Production must acquire
// exclusive device ownership before constructing either the gate or Manager.
type Manager struct {
	mu            sync.Mutex
	dir           string
	gate          admission
	profiles      map[string]*profile
	plans         map[string]workspace.ApplyPlan
	nextHandoff   time.Time
	now           func() time.Time
	retainedLimit int
	closed        bool
}

func New(dir string, gate *transferlimit.Gate) (*Manager, error) {
	if gate == nil {
		return nil, errors.New("invalid channel configuration")
	}
	return newManager(dir, gate)
}
func newManager(dir string, gate admission) (*Manager, error) {
	if gate == nil || !filepath.IsAbs(dir) {
		return nil, errors.New("invalid channel configuration")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	m := &Manager{dir: dir, gate: gate, profiles: map[string]*profile{}, plans: map[string]workspace.ApplyPlan{}, now: time.Now, retainedLimit: maxRetainedBytes}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

// Do keeps ownership through the response handoff, then starts an additional
// completion cooldown. A delayed HTTP writer cannot stockpile admissions.
// emit must finish the actual response write before it returns.
func (m *Manager) Do(ctx context.Context, request Request, emit func(Response) error) error {
	if !m.mu.TryLock() {
		return failure("BUSY", "Другая операция ещё выполняется.")
	}
	defer m.mu.Unlock()
	if m.closed {
		return failure("CHANNEL_CLOSED", "Сеанс figmobil завершается.")
	}
	if err := ctx.Err(); err != nil {
		return failure("CANCELLED", "Операция отменена до начала.")
	}
	// Existing immutable payload bytes are shared; mutable protocol state is
	// copy-on-write so failed validation/persistence cannot advance live trust/base.
	previous := m.profiles[request.ProfileID]
	committed := false
	defer func() {
		if !committed && previous != nil {
			m.profiles[request.ProfileID] = previous
			delete(m.plans, request.ProfileID)
		}
	}()
	if previous != nil && request.Action != "state" {
		copy := *previous
		copy.History = append([]string{}, previous.History...)
		if previous.Outgoing != nil {
			item := *previous.Outgoing
			copy.Outgoing = &item
		}
		if previous.Inbox != nil {
			item := *previous.Inbox
			item.Chunks = map[int][]byte{}
			for index, chunk := range previous.Inbox.Chunks {
				item.Chunks[index] = chunk
			}
			copy.Inbox = &item
		}
		m.profiles[request.ProfileID] = &copy
	}
	response, err := m.handle(ctx, request)
	if err != nil {
		return err
	}
	committed = true
	if response.Text != "" {
		defer func() { m.nextHandoff = m.now().Add(transferlimit.MinInterval) }()
	}
	return emit(response)
}

// Close drains the current operation and rejects handlers arriving after HTTP
// shutdown timed out. Only then may the device-owner lease be released.
func (m *Manager) Close() { m.mu.Lock(); m.closed = true; m.mu.Unlock() }
func (m *Manager) journal(p *profile) string {
	return filepath.Join(m.dir, "journal-"+p.ID, "journal.json")
}
func (m *Manager) recovery(p *profile) bool {
	_, err := os.Stat(m.journal(p))
	return err == nil || !errors.Is(err, os.ErrNotExist)
}
func (m *Manager) view(p *profile) Response {
	r := Response{Profiles: []Summary{}}
	for _, item := range m.profiles {
		r.Profiles = append(r.Profiles, Summary{item.ID, item.Name})
	}
	sort.Slice(r.Profiles, func(i, j int) bool { return r.Profiles[i].ID < r.Profiles[j].ID })
	if p == nil {
		return r
	}
	v := &ProfileView{ID: p.ID, Name: p.Name, DeviceName: p.DeviceName, Root: p.Root, Excludes: append([]string{}, p.Excludes...), Confirmed: p.Confirmed, PeerConfirmed: p.PeerConfirmed, Stage: "awaiting-peer", NeedsRecovery: m.recovery(p)}
	if p.Peer.Encryption != "" {
		identity, err := secure.Restore(p.Private)
		if err == nil {
			v.VerificationCode = verificationCode(p, identity)
		}
		v.Stage = "verify"
	}
	if p.Confirmed && p.PeerConfirmed {
		v.Stage = "paired"
	}
	if p.Base != nil {
		v.BaseID = p.Base.ID
	}
	if p.Outgoing != nil {
		v.Outgoing = &OutgoingView{p.Outgoing.Kind, partCount(p.Outgoing.Message), p.Outgoing.Next, p.Outgoing.AwaitingAck}
		if p.Outgoing.AwaitingAck {
			v.Stage = "awaiting-ack"
		}
	}
	if p.Inbox != nil {
		v.Incoming = &IncomingView{ID: p.Inbox.ID, Received: len(p.Inbox.Chunks), Total: p.Inbox.Total}
	}
	if p.Pending != nil {
		v.Incoming = &IncomingView{ID: p.Pending.ID, Ready: true, Kind: "data", Received: 1, Total: 1}
		v.Stage = "incoming"
		if p.MergeRequired {
			v.Stage = "merge-required"
		}
	}
	if v.NeedsRecovery {
		v.Stage = "recovery"
	}
	r.Profile = v
	return r
}
func validLabel(s string) bool {
	return len(strings.TrimSpace(s)) > 0 && len(s) <= 100 && !strings.ContainsAny(s, "\r\n\x00")
}
func (m *Manager) initial(ctx context.Context, r Request) (*profile, error) {
	if len(m.profiles) >= maxProfiles {
		return nil, failure("PROFILE_LIMIT", "Достигнут предел профилей.")
	}
	if !validLabel(r.Name) || !validLabel(r.DeviceName) || rootOverlaps(r.Root, m.dir) {
		return nil, failure("PROFILE_INPUT", "Укажите имя, имя ноутбука и отдельную папку проекта.")
	}
	snapshot, err := compare.Capture(ctx, r.Root, r.Excludes)
	if err != nil {
		return nil, failure("PROJECT_SCAN", "Не удалось проверить состав проекта. Проверьте путь, исключения и пределы чтения.")
	}
	identity, err := secure.Generate()
	if err != nil {
		return nil, failure("KEY_GENERATION", "Не удалось создать ключи устройства.")
	}
	private, err := identity.Protect()
	if err != nil {
		return nil, failure("KEY_PROTECTION", "Windows не удалось защитить ключи.")
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	inviteID, err := randomID()
	if err != nil {
		return nil, err
	}
	return &profile{Version: 1, ID: id, InviteID: inviteID, Name: strings.TrimSpace(r.Name), DeviceName: strings.TrimSpace(r.DeviceName), Root: snapshot.Root, Excludes: snapshot.Manifest.Excludes, Private: private, Originator: true, History: []string{}}, nil
}
func (m *Manager) queuePublic(p *profile, kind string, data []byte) {
	p.Outgoing = &outgoing{Kind: kind, Message: data, Digest: digest(data)}
}
func (m *Manager) queuePacket(p *profile, identity *secure.Identity, msg packet) error {
	data, err := sealedWire(p, identity, &msg)
	if err != nil {
		return failure("PACKAGE_BUILD", "Не удалось подготовить пакет в допустимых пределах.")
	}
	p.Outgoing = &outgoing{Kind: msg.Type, Message: data, Digest: digest(data), PacketID: msg.ID, Manifest: msg.Manifest, BaseID: msg.BaseID, ReplyTo: msg.ReplyTo, AwaitingAck: msg.Type == "data"}
	return nil
}
func (m *Manager) handle(ctx context.Context, r Request) (Response, error) {
	if len(r.Text) > 250000 || len(r.Root) > 32768 || len(r.Excludes) > 256 || len(r.Deletes) > 5000 {
		return Response{}, failure("INPUT_LIMIT", "Превышен размер параметров.")
	}
	var p *profile
	if r.ProfileID != "" {
		p = m.profiles[r.ProfileID]
		if p == nil {
			return Response{}, failure("PROFILE_NOT_FOUND", "Профиль не найден.")
		}
	}
	if r.Action == "state" {
		if p == nil && len(m.profiles) > 0 {
			keys := make([]string, 0, len(m.profiles))
			for id := range m.profiles {
				keys = append(keys, id)
			}
			sort.Strings(keys)
			p = m.profiles[keys[0]]
		}
		return m.view(p), nil
	}
	if r.Action == "create" || r.Action == "join" {
		if p != nil {
			return Response{}, failure("PROFILE_INPUT", "Создайте отдельный профиль.")
		}
		created, err := m.initial(ctx, r)
		if err != nil {
			return Response{}, err
		}
		identity, err := secure.Restore(created.Private)
		if err != nil {
			return Response{}, err
		}
		if r.Action == "create" {
			data, err := publicWire(identity, "invite", created.ID, created.InviteID, invitation{created.DeviceName})
			if err != nil {
				return Response{}, err
			}
			created.Offer = data
			m.queuePublic(created, "invite", data)
		} else {
			var a assembly
			data, err := a.add(r.Text)
			if err != nil || data == nil {
				return Response{}, failure("INVITATION_FORMAT", "Нужно полное приглашение figmobil из текстового слоя Figma.")
			}
			var w wire
			if strictJSON(data, &w) != nil || w.Kind != "invite" || verifyPublic(w) != nil {
				return Response{}, failure("INVITATION_INVALID", "Приглашение повреждено или имеет неизвестный формат.")
			}
			var body invitation
			if strictJSON(w.Body, &body) != nil || !validLabel(body.DeviceName) {
				return Response{}, failure("INVITATION_INVALID", "Некорректные данные приглашения.")
			}
			if m.profiles[w.ProjectID] != nil {
				return Response{}, failure("PROFILE_EXISTS", "Этот проект уже подключён.")
			}
			created.ID = w.ProjectID
			created.InviteID = w.InviteID
			created.Originator = false
			created.Offer = data
			created.Peer = w.Keys
			created.PeerName = body.DeviceName
			answer, err := publicWire(identity, "reply", created.ID, created.InviteID, reply{created.DeviceName, digest(data), w.Keys})
			if err != nil {
				return Response{}, err
			}
			m.queuePublic(created, "reply", answer)
		}
		m.profiles[created.ID] = created
		if err := m.checkRetained(); err != nil {
			delete(m.profiles, created.ID)
			return Response{}, failure("RESOURCE_LIMIT", "Превышен общий объём пакетов.")
		}
		if err := m.save(created); err != nil {
			delete(m.profiles, created.ID)
			return Response{}, failure("STATE_WRITE", "Не удалось сохранить защищённый профиль.")
		}
		response := m.view(created)
		response.Message = "Профиль создан. Выдайте текст подключения по кнопке и перенесите через Figma."
		return response, nil
	}
	if p == nil {
		return Response{}, failure("PROFILE_REQUIRED", "Сначала выберите профиль.")
	}
	if m.recovery(p) && r.Action != "recover" {
		return Response{}, failure("RECOVERY_REQUIRED", "Незавершённая запись требует явного восстановления.")
	}
	identity, err := secure.Restore(p.Private)
	if err != nil {
		return Response{}, failure("KEY_PROTECTION", "Не удалось открыть защищённые ключи Windows.")
	}
	response := Response{}
	switch r.Action {
	case "next", "repeat":
		if p.Outgoing == nil {
			return Response{}, failure("OUTBOX_EMPTY", "Нет подготовленного текста.")
		}
		index := p.Outgoing.Next + 1
		if r.Action == "repeat" {
			index = r.PartNumber
			if index < 1 || index > p.Outgoing.Next {
				return Response{}, failure("PART_NUMBER", "Повторить можно только уже выданную часть.")
			}
		}
		text, err := frameAtID(p.Outgoing.Message, p.Outgoing.Digest, index)
		if err != nil {
			return Response{}, failure("PART_NUMBER", "Все части выданы. Можно повторить выбранную часть.")
		}
		if delay := m.nextHandoff.Sub(m.now()); delay > 0 {
			return Response{}, &Error{Code: "RATE_LIMIT", Message: "Дождитесь следующей разрешённой выдачи.", RetryAfterMs: delay.Milliseconds() + 1}
		}
		decision, err := m.gate.TryAcquire([]byte(text))
		if err != nil {
			return Response{}, failure("RATE_STATE", "Лимитер остановил выдачу.")
		}
		if !decision.Allowed {
			return Response{}, &Error{Code: "RATE_LIMIT", Message: "Дождитесь следующей разрешённой выдачи.", RetryAfterMs: decision.RetryAfter.Milliseconds() + 1}
		}
		if r.Action == "next" {
			p.Outgoing.Next = index
		}
		if err := m.save(p); err != nil {
			return Response{}, failure("STATE_WRITE", "Не удалось сохранить выдачу; квота остаётся использованной.")
		}
		response.Text = text
	case "receive":
		delete(m.plans, p.ID)
		if p.Inbox == nil {
			p.Inbox = &assembly{}
		}
		data, err := p.Inbox.add(r.Text)
		if err != nil {
			return Response{}, failure("PART_INVALID", "Повреждённая часть или другая передача. Проверьте текст; для смены передачи явно сбросьте приём.")
		}
		if m.checkRetained() != nil {
			p.Inbox = nil
			return Response{}, failure("RESOURCE_LIMIT", "Превышен общий объём незавершённых пакетов.")
		}
		if data != nil {
			if err := m.receive(p, identity, data); err != nil {
				p.Inbox = nil
				return Response{}, err
			}
			p.Inbox = nil
		}
	case "confirm":
		if p.Peer.Encryption == "" || p.Confirmed || r.Code != verificationCode(p, identity) {
			return Response{}, failure("VERIFICATION_CODE", "Сначала сверьте одинаковый код на двух экранах figmobil.")
		}
		if p.Outgoing != nil && p.Outgoing.Next < partCount(p.Outgoing.Message) {
			return Response{}, failure("OUTBOX_PENDING", "Сначала выдайте ответ подключения через Figma.")
		}
		p.Confirmed = true
		if err := m.queuePacket(p, identity, packet{Type: "confirm"}); err != nil {
			p.Confirmed = false
			return Response{}, err
		}
	case "export", "resolve":
		if !p.Confirmed || !p.PeerConfirmed {
			return Response{}, failure("PAIR_NOT_CONFIRMED", "Подтвердите код на обоих ноутбуках и обменяйтесь подтверждениями через Figma.")
		}
		boundResolution := r.Action == "resolve" && p.Pending != nil && p.Outgoing != nil && p.Pending.ReplyTo == p.Outgoing.PacketID
		if p.Outgoing != nil && ((p.Outgoing.AwaitingAck && !boundResolution) || p.Outgoing.Next < partCount(p.Outgoing.Message)) {
			return Response{}, failure("OUTBOX_PENDING", "Завершите текущую выдачу и получите подтверждение применения.")
		}
		if r.Action == "export" && p.Pending != nil {
			return Response{}, failure("IMPORT_PENDING", "Сначала рассмотрите входящий пакет или явно экспортируйте результат ручного слияния.")
		}
		if r.Action == "resolve" && p.Pending == nil {
			return Response{}, failure("IMPORT_REQUIRED", "Нет входящей версии для результата слияния.")
		}
		snapshot, err := compare.Capture(ctx, p.Root, p.Excludes)
		if err != nil {
			return Response{}, failure("PROJECT_SCAN", "Не удалось прочитать проект целиком. Проверьте исключения и пределы.")
		}
		if !snapshot.Manifest.Policy.Supported {
			return Response{}, failure("GIT_POLICY", "Правила Git пока не поддерживаются; перенос остановлен без изменения настроек.")
		}
		baseID := ""
		deleteBase := p.Base
		if p.Base != nil {
			baseID = p.Base.ID
		}
		replyTo := ""
		if r.Action == "resolve" {
			baseID = p.Pending.Manifest.ID
			replyTo = p.Pending.ID
			deleteBase = p.Pending.Manifest
		}
		if len(r.Deletes) > 0 && deleteBase == nil {
			return Response{}, failure("BASE_REQUIRED", "Удаления требуют подтверждённой общей базы.")
		}
		seenDeletes := make(map[string]bool, len(r.Deletes))
		for _, path := range r.Deletes {
			if seenDeletes[path] {
				return Response{}, failure("DELETE_INTENT", "Удаление указано повторно.")
			}
			seenDeletes[path] = true
			if _, ok := deleteBase.Files[path]; !ok {
				return Response{}, failure("DELETE_INTENT", "Удаление отсутствует в общей базе.")
			}
			if _, ok := snapshot.Manifest.Files[path]; ok {
				return Response{}, failure("DELETE_INTENT", "Удаляемый файл ещё существует локально.")
			}
		}
		if err := m.queuePacket(p, identity, packet{Type: "data", BaseID: baseID, ReplyTo: replyTo, Manifest: &snapshot.Manifest, Files: snapshot.Files, Deletes: r.Deletes}); err != nil {
			return Response{}, err
		}
		if r.Action == "resolve" {
			remember(p, p.PendingDigest)
			p.Pending = nil
			p.PendingDigest = ""
			p.MergeRequired = false
		}
		delete(m.plans, p.ID)
	case "preview":
		if p.Pending == nil {
			return Response{}, failure("IMPORT_REQUIRED", "Сначала добавьте все части входящего пакета.")
		}
		plan, err := m.makePlan(ctx, p)
		if err != nil {
			return Response{}, err
		}
		m.plans[p.ID] = plan
		p.MergeRequired = !plan.CanApply
		preview := &Preview{ID: plan.ID, CanApply: plan.CanApply, Items: []PreviewItem{}}
		for _, item := range plan.Items {
			preview.Items = append(preview.Items, PreviewItem{item.Path, item.Status, item.Action, item.Reason})
		}
		response.Plan = preview
	case "apply":
		plan, ok := m.plans[p.ID]
		if !ok || r.PlanID != plan.ID || p.Pending == nil {
			return Response{}, failure("PLAN_REQUIRED", "Нужен актуальный предпросмотр и его подтверждение.")
		}
		delete(m.plans, p.ID)
		incoming := compare.Snapshot{Manifest: *p.Pending.Manifest, Files: p.Pending.Files}
		result, err := workspace.Apply(ctx, plan, incoming, filepath.Dir(m.journal(p)))
		if err != nil {
			return Response{}, failure("APPLY_STOPPED", fmt.Sprintf("Применение остановлено (%s). Проверьте состояние восстановления; подтверждение отправителю не выдано.", result.Status))
		}
		actual, err := compare.Capture(ctx, p.Root, p.Excludes)
		if err != nil {
			return Response{}, failure("VERIFY_RESULT", "Не удалось проверить результат записи. Подтверждение не сформировано.")
		}
		if actual.Manifest.ID != p.Pending.Manifest.ID {
			p.MergeRequired = true
			response.Message = "Входящие изменения применены; остались собственные изменения. Общая база пока прежняя. Экспортируйте проверенный результат ручного слияния."
			break
		}
		accepted := p.Pending
		acceptedManifest := *accepted.Manifest
		if err := m.queuePacket(p, identity, packet{Type: "ack", AckOf: accepted.ID, AcceptedID: acceptedManifest.ID, BaseID: accepted.BaseID}); err != nil {
			return Response{}, err
		}
		p.Base = &acceptedManifest
		remember(p, p.PendingDigest)
		p.Pending = nil
		p.PendingDigest = ""
		p.MergeRequired = false
	case "recover":
		delete(m.plans, p.ID)
		result, err := workspace.Recover(ctx, m.journal(p))
		if err != nil {
			return Response{}, failure("RECOVERY_STOPPED", "Восстановление остановлено: файлы могли измениться. Не перезаписывайте их; сохранены данные восстановления.")
		}
		response.Message = "Восстановление: " + result.Status + ". Сделайте новый предпросмотр перед применением."
	case "discard":
		p.Inbox = nil
		p.Pending = nil
		p.PendingDigest = ""
		p.MergeRequired = false
		delete(m.plans, p.ID)
		response.Message = "Незавершённый входящий пакет сброшен. Рабочие файлы и общая база сохранены."
	default:
		return Response{}, failure("ACTION_UNKNOWN", "Неизвестное действие.")
	}
	if err := m.checkRetained(); err != nil {
		return Response{}, failure("RESOURCE_LIMIT", "Превышен общий объём пакетов.")
	}
	if r.Action != "next" && r.Action != "repeat" {
		if err := m.save(p); err != nil {
			return Response{}, failure("STATE_WRITE", "Не удалось сохранить состояние. Подтверждение не выдано; проверьте локальные файлы перед повтором.")
		}
	}
	view := m.view(p)
	view.Text = response.Text
	view.Plan = response.Plan
	view.Message = response.Message
	return view, nil
}
