package main

import "testing"

func TestParseLiveUsage(t *testing.T) {
	body := []byte(`{"usage":{"rolling":{"status":"ok","percent":0,"resetsAt":"2026-11-10T22:16:16.000Z"},"weekly":{"status":"ok","percent":5,"resetsAt":"2026-11-16T00:00:00.000Z"},"monthly":{"status":"ok","percent":74,"resetsAt":"2026-11-26T16:34:31.000Z"}}}`)
	buckets, err := parseLiveUsage(body)
	if err != nil {
		t.Fatalf("parseLiveUsage: %v", err)
	}
	want := []struct {
		window    string
		remaining float64
		reset     string
	}{
		{"5h", 1, "2026-11-10T22:16:16Z"},
		{"weekly", 0.95, "2026-11-16T00:00:00Z"},
		{"monthly", 0.26, "2026-11-26T16:34:31Z"},
	}
	if len(buckets) != len(want) {
		t.Fatalf("got %d buckets, want %d", len(buckets), len(want))
	}
	for i, w := range want {
		b := buckets[i]
		if b.Window != w.window || b.RemainingFraction != w.remaining || b.ResetTime != w.reset {
			t.Errorf("bucket %d = {%s %v %s}, want {%s %v %s}", i, b.Window, b.RemainingFraction, b.ResetTime, w.window, w.remaining, w.reset)
		}
	}
}

func TestParseLiveUsageRateLimited(t *testing.T) {
	buckets, err := parseLiveUsage([]byte(`{"usage":{"rolling":{"status":"rate-limited","resetsAt":"2026-11-10T22:16:16Z"}}}`))
	if err != nil {
		t.Fatalf("parseLiveUsage: %v", err)
	}
	if len(buckets) != 1 || buckets[0].RemainingFraction != 0 {
		t.Fatalf("rate-limited window should report 0 remaining, got %+v", buckets)
	}
}

func TestParseLiveUsageRejectsBadInput(t *testing.T) {
	for _, body := range []string{
		`not json`,
		`{"usage":{}}`,
		`{"usage":{"weekly":{"status":"ok"}}}`,
		`{"usage":{"weekly":{"status":"ok","percent":5,"resetsAt":"tomorrow"}}}`,
	} {
		if _, err := parseLiveUsage([]byte(body)); err == nil {
			t.Errorf("expected error for %s", body)
		}
	}
}
