package channel

import (
	"context"
	"encoding/json"
	"reflect"

	"figmobil/internal/compare"
	"figmobil/internal/secure"
	"figmobil/internal/workspace"
)

func (m *Manager) receive(p *profile, identity *secure.Identity, data []byte) error {
	id := digest(data)
	if p.Pending != nil && p.PendingDigest == id {
		return nil
	}
	for _, seen := range p.History {
		if seen == id {
			return nil
		}
	}
	var w wire
	if strictJSON(data, &w) != nil || w.Version != 1 || w.ProjectID != p.ID || w.InviteID != p.InviteID {
		return failure("PACKAGE_PROJECT", "Пакет относится к другому проекту или имеет неизвестный формат.")
	}
	if w.Kind == "reply" {
		if !p.Originator || p.Peer.Encryption != "" || verifyPublic(w) != nil {
			return failure("PAIR_REPLY", "Ответ подключения не подходит к этому приглашению.")
		}
		var body reply
		if strictJSON(w.Body, &body) != nil || body.InviteHash != digest(p.Offer) || body.Inviter != identity.Public() || !validLabel(body.DeviceName) {
			return failure("PAIR_REPLY", "Ответ не связан с исходным приглашением.")
		}
		p.Peer = w.Keys
		p.PeerName = body.DeviceName
	} else if w.Kind == "sealed" {
		if p.Peer.Encryption == "" || w.Keys != (secure.PublicKeys{}) || w.Signature != "" {
			return failure("PAIR_REQUIRED", "Сначала завершите обмен приглашением и ответом.")
		}
		plain, err := identity.Open(p.Peer, w.Body, MaxPlainBytes)
		if err != nil {
			return failure("PACKAGE_AUTHENTICATION", "Пакет повреждён, адресован другому устройству или отправитель не подтверждён.")
		}
		defer clear(plain)
		var msg packet
		if strictJSON(plain, &msg) != nil || msg.Version != 1 || !hexID(msg.ID, 32) || msg.ProjectID != p.ID || msg.InviteID != p.InviteID || msg.Transcript != verificationCode(p, identity) {
			return failure("PACKAGE_BINDING", "Неверная связь пакета с устройствами или проектом.")
		}
		switch msg.Type {
		case "confirm":
			if msg.Manifest != nil || len(msg.Files) > 0 || msg.AckOf != "" || msg.BaseID != "" || msg.ReplyTo != "" || msg.AcceptedID != "" || len(msg.Deletes) > 0 {
				return failure("PACKAGE_FORMAT", "Неверное подтверждение устройств.")
			}
			p.PeerConfirmed = true
		case "data":
			if !p.Confirmed || !p.PeerConfirmed {
				return failure("PAIR_NOT_CONFIRMED", "Сначала сверьте код на двух экранах и обменяйтесь подтверждениями.")
			}
			if p.Pending != nil {
				return failure("IMPORT_PENDING", "Уже есть входящий пакет. Рассмотрите или явно сбросьте его.")
			}
			if msg.Manifest == nil || msg.AckOf != "" || msg.AcceptedID != "" || compare.ValidateSnapshot(compare.Snapshot{Manifest: *msg.Manifest, Files: msg.Files}) != nil {
				return failure("MANIFEST_INVALID", "Состав или контрольные суммы файлов не прошли проверку.")
			}
			if !reflect.DeepEqual(normalizedExcludes(p.Excludes), normalizedExcludes(msg.Manifest.Excludes)) {
				return failure("SCOPE_MISMATCH", "Состав исключений на ноутбуках различается. Согласуйте одинаковый состав проекта.")
			}
			if err := m.checkIncomingBase(p, &msg); err != nil {
				return err
			}
			p.Pending = &msg
			p.PendingDigest = id
			p.MergeRequired = false
			return nil
		case "ack":
			out := p.Outgoing
			if !p.Confirmed || !p.PeerConfirmed || out == nil || !out.AwaitingAck || out.Manifest == nil || msg.AckOf != out.PacketID || msg.AcceptedID != out.Manifest.ID || msg.BaseID != out.BaseID || msg.Manifest != nil || len(msg.Files) > 0 || len(msg.Deletes) > 0 || msg.ReplyTo != "" {
				return failure("ACK_MISMATCH", "Подтверждение не соответствует ожидаемой версии. Общая база сохранена.")
			}
			accepted := *out.Manifest
			p.Base = &accepted
			p.Outgoing = nil
			p.Pending = nil
			p.PendingDigest = ""
			p.MergeRequired = false
			delete(m.plans, p.ID)
		default:
			return failure("PACKAGE_FORMAT", "Неизвестный тип зашифрованного сообщения.")
		}
	} else {
		return failure("PACKAGE_FORMAT", "Неизвестный тип сообщения.")
	}
	remember(p, id)
	return nil
}
func remember(p *profile, id string) {
	if id == "" {
		return
	}
	p.History = append(p.History, id)
	if len(p.History) > 128 {
		p.History = append([]string{}, p.History[len(p.History)-128:]...)
	}
}
func normalizedExcludes(values []string) string {
	data, _ := json.Marshal(append([]string{}, values...))
	return string(data)
}
func (m *Manager) checkIncomingBase(p *profile, msg *packet) error {
	if msg.ReplyTo != "" {
		if p.Outgoing == nil || !p.Outgoing.AwaitingAck || p.Outgoing.Manifest == nil || msg.ReplyTo != p.Outgoing.PacketID || msg.BaseID != p.Outgoing.Manifest.ID {
			return failure("REPLY_MISMATCH", "Результат слияния не связан с ожидающим исходящим пакетом.")
		}
		return nil
	}
	if p.Outgoing != nil && p.Outgoing.AwaitingAck {
		return failure("ACK_REQUIRED", "Сначала нужен ответ на текущую исходящую версию.")
	}
	if p.Base == nil {
		if msg.BaseID != "" {
			return failure("BASE_UNKNOWN", "На этом ноутбуке ещё нет указанной общей базы.")
		}
	} else if msg.BaseID != p.Base.ID {
		return failure("BASE_MISMATCH", "Общая база различается. Сначала доставьте подтверждение принятой версии.")
	}
	return nil
}
func (m *Manager) makePlan(ctx context.Context, p *profile) (workspace.ApplyPlan, error) {
	local, err := compare.Capture(ctx, p.Root, p.Excludes)
	if err != nil {
		return workspace.ApplyPlan{}, failure("PROJECT_SCAN", "Не удалось прочитать текущее состояние проекта.")
	}
	incoming := compare.Snapshot{Manifest: *p.Pending.Manifest, Files: p.Pending.Files}
	var base *compare.Manifest
	if p.Pending.ReplyTo != "" {
		if p.Outgoing == nil || p.Outgoing.Manifest == nil {
			return workspace.ApplyPlan{}, failure("BASE_UNKNOWN", "Исходная версия для результата слияния отсутствует.")
		}
		base = p.Outgoing.Manifest
	} else {
		base = p.Base
	}
	deletes := make([]workspace.DeleteIntent, 0, len(p.Pending.Deletes))
	seen := map[string]bool{}
	for _, path := range p.Pending.Deletes {
		if base == nil || seen[path] {
			return workspace.ApplyPlan{}, failure("DELETE_INTENT", "Некорректное явное удаление.")
		}
		metadata, ok := base.Files[path]
		if !ok {
			return workspace.ApplyPlan{}, failure("DELETE_INTENT", "Удаление не связано с общей базой.")
		}
		if _, ok := incoming.Manifest.Files[path]; ok {
			return workspace.ApplyPlan{}, failure("DELETE_INTENT", "В пакете одновременно файл и его удаление.")
		}
		seen[path] = true
		deletes = append(deletes, workspace.DeleteIntent{Path: path, ExpectedBaseSHA256: metadata.SHA256})
	}
	var plan workspace.ApplyPlan
	if base == nil {
		plan, err = workspace.PlanInitial(local, incoming)
	} else {
		plan, err = workspace.Plan(base, local, incoming, deletes)
	}
	if err != nil {
		return workspace.ApplyPlan{}, failure("PLAN_STOPPED", "Не удалось построить безопасный план. Проверьте базу и правила Git.")
	}
	return plan, nil
}
