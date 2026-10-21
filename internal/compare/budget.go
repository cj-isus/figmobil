package compare

import (
	"encoding/json"
	"fmt"
)

// This reserve covers the fixed JSON envelope, three scan counters, and the
// bounded status summary. Each variable item/warning is charged separately.
const metadataEnvelopeBytes int64 = 4096

func (b *scanBudget) reserveMetadata(bytes int64) error {
	if bytes < 0 || bytes > b.limits.MaxMetadataBytes-b.metadataBytes {
		return fail("REPORT_SIZE_LIMIT", fmt.Sprintf("Пути и метаданные сравнения превышают общий лимит %d байт. Выберите папки меньшего размера.", b.limits.MaxMetadataBytes))
	}
	b.metadataBytes += bytes
	return nil
}

func (b *scanBudget) reserveEntryPath(parent, name string) error {
	// Up to six JSON bytes per UTF-8 source byte bounds escaping; the fixed
	// allowance also charges retained names, observations, and directory keys.
	// This is a conservative operation budget, not a process heap measurement.
	return b.reserveMetadata(6*(int64(len(parent))+int64(len(name))+1) + 256)
}

func (b *scanBudget) reserveJSON(value any, extra int64) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fail("REPORT_ENCODING_FAILED", "Не удалось подготовить метаданные сравнения.")
	}
	return b.reserveMetadata(int64(len(data)) + extra)
}

func (b *scanBudget) appendWarning(target *[]Warning, warning Warning) error {
	// Scanner warnings acquire a root label after reading. Allow its maximum
	// length here, as well as the separator, before accumulating the warning.
	if err := b.reserveJSON(warning, 16); err != nil {
		return err
	}
	*target = append(*target, warning)
	return nil
}
