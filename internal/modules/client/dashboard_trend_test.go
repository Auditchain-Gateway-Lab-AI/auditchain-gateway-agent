package client

import (
	"testing"
	"time"
)

func TestParseDashboardTrendRange(t *testing.T) {
	tests := []struct {
		value       string
		name        string
		buckets     int
		duration    time.Duration
		wantSupport bool
	}{
		{value: "8h", name: "8H", buckets: 8, duration: 8 * time.Hour, wantSupport: true},
		{value: "24H", name: "24H", buckets: 12, duration: 24 * time.Hour, wantSupport: true},
		{value: "7D", name: "7D", buckets: 7, duration: 7 * 24 * time.Hour, wantSupport: true},
		{value: "30D", name: "30D", buckets: 10, duration: 30 * 24 * time.Hour, wantSupport: true},
		{value: "90D", wantSupport: false},
	}

	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			got, supported := parseDashboardTrendRange(test.value)
			if supported != test.wantSupport {
				t.Fatalf("supported = %t, want %t", supported, test.wantSupport)
			}
			if !supported {
				return
			}
			if got.name != test.name || got.bucketQty != test.buckets || got.duration != test.duration {
				t.Fatalf("range = %#v, want name=%q buckets=%d duration=%s", got, test.name, test.buckets, test.duration)
			}
		})
	}
}

func TestBuildDashboardTrendResponseIncludesEmptyBucketsAndAggregates(t *testing.T) {
	window, _ := parseDashboardTrendRange("8H")
	from := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	bucketDuration := time.Hour
	aggregates := []dashboardTrendAggregate{
		{
			BucketIndex: 2,
			TotalLogs:   12,
			Valid:       9,
			Tampered:    1,
			Pending:     1,
			Unavailable: 1,
			Insert:      4,
			Update:      5,
			Delete:      3,
		},
	}

	got := buildDashboardTrendResponse(window, from, from.Add(window.duration), bucketDuration, 3600, aggregates)
	if len(got.Points) != 8 {
		t.Fatalf("point count = %d, want 8", len(got.Points))
	}
	if got.Points[0].TotalLogs != 0 || !got.Points[0].BucketStart.Equal(from) {
		t.Fatalf("empty first bucket = %#v, want zero counts at %s", got.Points[0], from)
	}
	point := got.Points[2]
	if point.TotalLogs != 12 || point.Valid != 9 || point.Tampered != 1 || point.Pending != 1 || point.Unavailable != 1 {
		t.Fatalf("integrity aggregates = %#v, want the supplied counts", point)
	}
	if point.Insert != 4 || point.Update != 5 || point.Delete != 3 {
		t.Fatalf("activity aggregates = %#v, want insert=4 update=5 delete=3", point)
	}
}
