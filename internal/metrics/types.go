// Package metrics collects per-tool usage counters for serve mode and
// persists them to a sidecar file (metrics.json) next to the active config.
// Counters are bucketed per (date, namespace, server, tool) and hold call
// counts, outcome tallies, and a fixed-boundary latency histogram. The hard
// privacy rule: names, timestamps, durations, and outcomes only — never tool
// arguments, results, or error message bodies.
package metrics

import "time"

// Outcome classifies how a tool call ended.
type Outcome string

const (
	OutcomeOK        Outcome = "ok"         // upstream returned, isError=false
	OutcomeToolError Outcome = "tool_error" // upstream returned, isError=true
	OutcomeError     Outcome = "error"      // transport/internal/startup failure
	OutcomeTimeout   Outcome = "timeout"    // per-tool timeout hit
	OutcomeDenied    Outcome = "denied"     // permission check refused the call
)

// dateLayout is the day-bucket key format. Local time; ISO dates sort
// lexicographically, which pruning and range filters rely on.
const dateLayout = "2006-01-02"

// CallSample is the single unit handed to the Recorder. Deliberately flat —
// if OTel export is ever added, these fields map 1:1 onto span attributes.
type CallSample struct {
	Time      time.Time
	Namespace string        // "" when no namespace is active; store as ""
	Server    string        // config server name; "mcpmu" for manager tools
	Tool      string        // unqualified tool name
	Duration  time.Duration // 0 for denied calls
	Outcome   Outcome
}

// BucketKey identifies one daily counter row.
type BucketKey struct {
	Date      string // "2006-01-02", local time
	Namespace string
	Server    string
	Tool      string
}

// HistBoundsMs are the histogram upper bounds. Log-scale, chosen to bracket
// typical MCP tool calls (10ms local tools → 60s slow HTTP tools).
var HistBoundsMs = [...]uint64{10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000}

// histBuckets includes the +Inf overflow bucket after the last boundary.
const histBuckets = len(HistBoundsMs) + 1

// Counters is the accumulated value for one BucketKey.
type Counters struct {
	Calls    uint64 // total, all outcomes
	Outcomes map[Outcome]uint64
	// Latency aggregates cover dispatched calls only — denied calls never
	// reached an upstream and are excluded (see addSample).
	DurationMsSum uint64
	DurationMsMax uint64
	// Latency histogram. Hist[i] counts calls with duration <= HistBoundsMs[i];
	// the last element is the +Inf overflow.
	Hist [histBuckets]uint64
}

func newCounters() *Counters {
	return &Counters{Outcomes: make(map[Outcome]uint64)}
}

// histIndex returns the histogram bucket for a duration in milliseconds.
func histIndex(ms uint64) int {
	for i, bound := range HistBoundsMs {
		if ms <= bound {
			return i
		}
	}
	return len(HistBoundsMs)
}

// addSample accumulates one call into the counters. A denied call never
// reached an upstream, so it counts toward Calls and Outcomes but stays out of
// the latency aggregates — folding its zero duration in would drag p50/p95
// toward zero for a tool that is mostly refused.
func (c *Counters) addSample(durationMs uint64, outcome Outcome) {
	c.Calls++
	if c.Outcomes == nil {
		c.Outcomes = make(map[Outcome]uint64)
	}
	c.Outcomes[outcome]++
	if outcome == OutcomeDenied {
		return
	}
	c.DurationMsSum += durationMs
	c.DurationMsMax = max(c.DurationMsMax, durationMs)
	c.Hist[histIndex(durationMs)]++
}

// merge adds another counter set into this one (pure addition; max takes max).
func (c *Counters) merge(o *Counters) {
	c.Calls += o.Calls
	if c.Outcomes == nil {
		c.Outcomes = make(map[Outcome]uint64)
	}
	for outcome, n := range o.Outcomes {
		c.Outcomes[outcome] += n
	}
	c.DurationMsSum += o.DurationMsSum
	c.DurationMsMax = max(c.DurationMsMax, o.DurationMsMax)
	for i := range c.Hist {
		c.Hist[i] += o.Hist[i]
	}
}

// errors returns the failure count: everything that went wrong upstream or in
// transit, excluding permission denials (those are counted separately).
func (c *Counters) errors() uint64 {
	return c.Outcomes[OutcomeToolError] + c.Outcomes[OutcomeError] + c.Outcomes[OutcomeTimeout]
}

// timedCalls is the number of calls behind the latency aggregates: everything
// that was actually dispatched. Denied calls are the only outcome addSample
// keeps out of them, so subtracting them is exact.
func (c *Counters) timedCalls() uint64 {
	denied := c.Outcomes[OutcomeDenied]
	if denied >= c.Calls {
		return 0
	}
	return c.Calls - denied
}

// percentileMs estimates the p-th percentile (0 < p <= 1) latency by walking
// the histogram to the target cumulative count and linearly interpolating
// within the bucket. The overflow bucket interpolates between the last
// boundary and the observed max. Approximation is fine — this is a usage
// dashboard, not an SLO tool.
func (c *Counters) percentileMs(p float64) uint64 {
	var total uint64
	for _, n := range c.Hist {
		total += n
	}
	if total == 0 {
		return 0
	}
	target := p * float64(total)
	if target < 1 {
		target = 1
	}

	var cum uint64
	for i, n := range c.Hist {
		prev := cum
		cum += n
		if n == 0 || float64(cum) < target {
			continue
		}
		var lower uint64
		if i > 0 {
			lower = HistBoundsMs[i-1]
		}
		var upper uint64
		if i < len(HistBoundsMs) {
			upper = HistBoundsMs[i]
		} else {
			upper = max(c.DurationMsMax, HistBoundsMs[len(HistBoundsMs)-1])
		}
		frac := (target - float64(prev)) / float64(n)
		return lower + uint64(frac*float64(upper-lower)+0.5)
	}
	return c.DurationMsMax
}

// RecentCall is one row of the rolling feed. NEVER store arguments, results,
// or error message bodies — tool name + metadata only (privacy: arguments
// routinely contain secrets, tokens, and personal data).
type RecentCall struct {
	Time       time.Time `json:"time"`
	Namespace  string    `json:"namespace,omitempty"`
	Server     string    `json:"server"`
	Tool       string    `json:"tool"`
	DurationMs uint64    `json:"durationMs"`
	Outcome    Outcome   `json:"outcome"`
}

// durationMs converts a duration to whole milliseconds, clamping negatives.
func durationMs(d time.Duration) uint64 {
	ms := d.Milliseconds()
	if ms < 0 {
		return 0
	}
	return uint64(ms)
}
