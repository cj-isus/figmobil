package compare

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestMetadataBudgetIsSharedAcrossRootsAndCountsExclusions(t *testing.T) {
	req := roots(t)
	req.BaseRoot = ""
	write(t, req.LocalRoot, ".env.local", nil)
	limits := DefaultLimits()
	limits.MaxMetadataBytes = 5000
	first, err := Run(context.Background(), req, limits)
	if err != nil || len(first.Items) != 0 || len(first.Warnings) != 1 || !first.Complete {
		t.Fatalf("one excluded zero-byte file should fit: %+v, %v", first, err)
	}
	// Both copies separately fit this limit. Together their paths/warnings must
	// consume the same budget even though no source bytes or items are produced.
	write(t, req.IncomingRoot, ".env.local", nil)
	report, err := Run(context.Background(), req, limits)
	if ErrorCode(err) != "REPORT_SIZE_LIMIT" || report.Complete || report.Items != nil || report.Warnings != nil {
		t.Fatalf("expected closed failure without partial report: %+v, %v", report, err)
	}
}

func TestMetadataBudgetChargesPathsBeforeWarningsOrItems(t *testing.T) {
	req := roots(t)
	req.BaseRoot = ""
	name := ".env." + strings.Repeat("x", 180)
	write(t, req.LocalRoot, name, nil)
	limits := DefaultLimits()
	limits.MaxMetadataBytes = 5000
	report, err := Run(context.Background(), req, limits)
	if ErrorCode(err) != "REPORT_SIZE_LIMIT" || report.Complete || report.Items != nil || report.Warnings != nil {
		t.Fatalf("long excluded path should exceed retained-path budget: %+v, %v", report, err)
	}
}

func TestMetadataBudgetAlsoBoundsMaterializedItems(t *testing.T) {
	req := roots(t)
	req.BaseRoot = ""
	write(t, req.LocalRoot, "a.txt", []byte("a"))
	write(t, req.IncomingRoot, "a.txt", []byte("a"))
	limits := DefaultLimits()
	limits.MaxMetadataBytes = 5000
	report, err := Run(context.Background(), req, limits)
	if ErrorCode(err) != "REPORT_SIZE_LIMIT" || report.Complete || report.Items != nil {
		t.Fatalf("items must not bypass path budget: %+v, %v", report, err)
	}
	limits.MaxMetadataBytes = 8192
	report, err = Run(context.Background(), req, limits)
	if err != nil || len(report.Items) != 1 || !report.Complete {
		t.Fatalf("larger bounded budget should fit: %+v, %v", report, err)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) > limits.MaxMetadataBytes {
		t.Fatalf("serialized report %d exceeds metadata limit %d", len(data), limits.MaxMetadataBytes)
	}
}

func TestMetadataLimitValidationAndOmittedDefault(t *testing.T) {
	req := roots(t)
	for _, limit := range []int64{-1, 4095, (32 << 20) + 1} {
		limits := DefaultLimits()
		limits.MaxMetadataBytes = limit
		if _, err := Run(context.Background(), req, limits); ErrorCode(err) != "INVALID_LIMITS" {
			t.Fatalf("invalid metadata limit %d: %v", limit, err)
		}
	}
	limits := DefaultLimits()
	limits.MaxMetadataBytes = 0
	write(t, req.LocalRoot, "a.txt", nil)
	if _, err := Run(context.Background(), req, limits); err != nil {
		t.Fatalf("omitted metadata budget must use bounded default: %v", err)
	}
}

func TestReportJSONBoundIncludesEscapingAndStructuralWarnings(t *testing.T) {
	req := roots(t)
	req.BaseRoot = ""
	write(t, req.LocalRoot, "目录&A/child.txt", nil)
	write(t, req.IncomingRoot, "目录&a", nil)
	write(t, req.LocalRoot, ".env.&private", nil)
	limits := DefaultLimits()
	limits.MaxMetadataBytes = 8192
	report, err := Run(context.Background(), req, limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Warnings) < 3 || len(report.Items) != 2 {
		t.Fatalf("fixture did not exercise collision and excluded warnings: %+v", report)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) > limits.MaxMetadataBytes {
		t.Fatalf("serialized report %d exceeds metadata budget %d", len(data), limits.MaxMetadataBytes)
	}
}
