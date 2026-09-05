package metrics

import (
	"fmt"
	"testing"
	"time"
)

func BenchmarkMetricsQueries(b *testing.B) {
	store := NewStore()
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	for day := range 60 {
		for tool := range 1000 {
			store.Rows[BucketKey{Date: start.AddDate(0, 0, day).Format(dateLayout), Namespace: "work", Server: fmt.Sprintf("server%d", tool%10), Tool: fmt.Sprintf("tool%d", tool)}] = &Counters{Calls: 10, DurationMsSum: 1000, DurationMsMax: 100, Outcomes: map[Outcome]uint64{OutcomeOK: 10}}
		}
	}
	filter := Filter{Namespace: "work"}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = store.ToolTable(filter)
		_ = store.DailyTotals(filter)
		_ = store.Summary(filter)
	}
}
