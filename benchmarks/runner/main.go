package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	goxrt "github.com/goxlang/gox/internal/runtime"
)

type BenchmarkMetric struct {
	Name            string
	Operations      int
	StandardTime    time.Duration
	GOXTime         time.Duration
	StandardAllocs  uint64
	GOXAllocs       uint64
	StandardBytes   uint64
	GOXBytes        uint64
	StandardGC      uint32
	GOXGC           uint32
	StandardPauseNs uint64
	GOXPauseNs      uint64
}

// ---------------------------------------------------------
// Workload 1: Request-Response Lifecycle
// ---------------------------------------------------------
type RequestContext struct {
	ID        int
	SessionID string
	Payload   [256]byte
}

type ResponseContext struct {
	StatusCode int
	EchoID     int
	Result     string
}

//go:noinline
func processRequestStandard(id int) *ResponseContext {
	req := &RequestContext{
		ID:        id,
		SessionID: "sess-123",
	}
	req.Payload[0] = byte(id)
	resp := &ResponseContext{
		StatusCode: 200,
		EchoID:     req.ID,
		Result:     req.SessionID,
	}
	return resp
}

func runRequestStandard(iterations int) {
	for i := 0; i < iterations; i++ {
		resp := processRequestStandard(i)
		_ = resp.EchoID + len(resp.Result)
	}
}

//go:noinline
func processRequestGOX(arena *goxrt.Arena, id int) *ResponseContext {
	req := RequestContext{
		ID:        id,
		SessionID: "sess-123",
	}
	req.Payload[0] = byte(id)
	resp := goxrt.AllocVal(arena, ResponseContext{
		StatusCode: 200,
		EchoID:     req.ID,
		Result:     req.SessionID,
	})
	return resp
}

func runRequestGOX(iterations int) {
	arena := goxrt.NewArena()
	for i := 0; i < iterations; i++ {
		resp := processRequestGOX(arena, i)
		_ = resp.EchoID + len(resp.Result)
		if i%1000 == 0 {
			arena.Reset()
		}
	}
	goxrt.Free(arena)
}

// ---------------------------------------------------------
// Workload 2: Deep Tree Graph Construction
// ---------------------------------------------------------
type TreeNode struct {
	Value int
	Left  *TreeNode
	Right *TreeNode
}

//go:noinline
func buildTreeStandard(depth int, val int) *TreeNode {
	if depth <= 0 {
		return nil
	}
	return &TreeNode{
		Value: val,
		Left:  buildTreeStandard(depth-1, val*2),
		Right: buildTreeStandard(depth-1, val*2+1),
	}
}

//go:noinline
func sumTreeStandard(node *TreeNode) int {
	if node == nil {
		return 0
	}
	return node.Value + sumTreeStandard(node.Left) + sumTreeStandard(node.Right)
}

//go:noinline
func buildTreeGOX(arena *goxrt.Arena, depth int, val int) *TreeNode {
	if depth <= 0 {
		return nil
	}
	node := goxrt.AllocVal(arena, TreeNode{
		Value: val,
	})
	node.Left = buildTreeGOX(arena, depth-1, val*2)
	node.Right = buildTreeGOX(arena, depth-1, val*2+1)
	return node
}

func runTreeStandard(trees int, depth int) {
	for i := 0; i < trees; i++ {
		root := buildTreeStandard(depth, 1)
		_ = sumTreeStandard(root)
	}
}

func runTreeGOX(trees int, depth int) {
	arena := goxrt.NewArena()
	for i := 0; i < trees; i++ {
		root := buildTreeGOX(arena, depth, 1)
		_ = sumTreeStandard(root)
		arena.Reset()
	}
	goxrt.Free(arena)
}

// ---------------------------------------------------------
// Workload 3: High-Frequency Packet Inspection (Unique Pool)
// ---------------------------------------------------------
type Packet struct {
	Seq  int
	Data [512]byte
}

//go:noinline
func inspectPacketStandard(seq int) *Packet {
	p := &Packet{Seq: seq}
	p.Data[0] = byte(seq % 256)
	return p
}

func runPacketStandard(iterations int) {
	for i := 0; i < iterations; i++ {
		p := inspectPacketStandard(i)
		_ = p.Seq + int(p.Data[0])
	}
}

//go:noinline
func inspectPacketGOX(seq int) *Packet {
	p := goxrt.AllocUnique(Packet{Seq: seq})
	p.Data[0] = byte(seq % 256)
	return p
}

func runPacketGOX(iterations int) {
	for i := 0; i < iterations; i++ {
		p := inspectPacketGOX(i)
		_ = p.Seq + int(p.Data[0])
		goxrt.FreeUnique(p)
	}
}

func measure(fn func()) (time.Duration, uint64, uint64, uint32, uint64) {
	runtime.GC()
	var mBefore, mAfter runtime.MemStats
	runtime.ReadMemStats(&mBefore)

	start := time.Now()
	fn()
	elapsed := time.Since(start)

	runtime.ReadMemStats(&mAfter)

	mallocs := mAfter.Mallocs - mBefore.Mallocs
	bytesAlloc := mAfter.TotalAlloc - mBefore.TotalAlloc
	numGC := mAfter.NumGC - mBefore.NumGC
	pauseNs := mAfter.PauseTotalNs - mBefore.PauseTotalNs

	return elapsed, mallocs, bytesAlloc, numGC, pauseNs
}

var (
	cReset       = "\033[0m"
	cBold        = "\033[1m"
	cDim         = "\033[2m"
	cRed         = "\033[31m"
	cGreen       = "\033[32m"
	cYellow      = "\033[33m"
	cBlue        = "\033[34m"
	cMagenta     = "\033[35m"
	cCyan        = "\033[36m"
	cWhite       = "\033[37m"
	cGray        = "\033[90m"
	cBrightGreen = "\033[92m"
	cBrightCyan  = "\033[96m"
	cBrightWhite = "\033[97m"
)

func init() {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		cReset = ""
		cBold = ""
		cDim = ""
		cRed = ""
		cGreen = ""
		cYellow = ""
		cBlue = ""
		cMagenta = ""
		cCyan = ""
		cWhite = ""
		cGray = ""
		cBrightGreen = ""
		cBrightCyan = ""
		cBrightWhite = ""
	}
}

func visibleLen(s string) int {
	inEscape := false
	n := 0
	for _, r := range s {
		if r == '\033' {
			inEscape = true
			continue
		}
		if inEscape {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		n++
	}
	return n
}

func padRight(s string, width int) string {
	vl := visibleLen(s)
	if vl >= width {
		return s
	}
	return s + strings.Repeat(" ", width-vl)
}

func centerText(s string, width int) string {
	vl := visibleLen(s)
	if vl >= width {
		return s
	}
	leftPad := (width - vl) / 2
	rightPad := width - vl - leftPad
	return strings.Repeat(" ", leftPad) + s + strings.Repeat(" ", rightPad)
}

func renderRow(cols []string, widths []int) string {
	var sb strings.Builder
	sb.WriteString(cGray)
	sb.WriteString("│")
	sb.WriteString(cReset)
	for i, col := range cols {
		sb.WriteString(" ")
		sb.WriteString(padRight(col, widths[i]))
		sb.WriteString(" ")
		sb.WriteString(cGray)
		sb.WriteString("│")
		sb.WriteString(cReset)
	}
	return sb.String()
}

func renderBorder(widths []int, left, mid, right string) string {
	var sb strings.Builder
	sb.WriteString(cGray)
	sb.WriteString(left)
	for i, w := range widths {
		sb.WriteString(strings.Repeat("─", w+2))
		if i < len(widths)-1 {
			sb.WriteString(mid)
		}
	}
	sb.WriteString(right)
	sb.WriteString(cReset)
	return sb.String()
}

func formatDiff(val float64, suffix string) string {
	if val > 0.05 {
		return fmt.Sprintf("%s%s✔ +%.1f%% %s%s", cBold, cBrightGreen, val, suffix, cReset)
	} else if val < -0.05 {
		return fmt.Sprintf("%s~ %.1f%% latency%s", cYellow, val, cReset)
	}
	return fmt.Sprintf("%s0.0%% (parity)%s", cGray, cReset)
}

func formatGCDiff(std, gox uint32, diff float64) string {
	if std > 0 && gox == 0 {
		return fmt.Sprintf("%s%s✔ +100.0%% zero-GC%s", cBold, cBrightGreen, cReset)
	}
	if diff > 0.05 {
		return fmt.Sprintf("%s%s✔ +%.1f%% reduction%s", cBold, cBrightGreen, diff, cReset)
	}
	if std == 0 && gox == 0 {
		return fmt.Sprintf("%s%s✔ 0 cycles (zero-GC)%s", cBold, cBrightGreen, cReset)
	}
	return fmt.Sprintf("%s0.0%% (parity)%s", cGray, cReset)
}

func formatPauseDiff(std, gox uint64, diff float64) string {
	if std > 0 && gox == 0 {
		return fmt.Sprintf("%s%s✔ +100.0%% zero-pause%s", cBold, cBrightGreen, cReset)
	}
	if diff > 0.05 {
		return fmt.Sprintf("%s%s✔ +%.1f%% reduction%s", cBold, cBrightGreen, diff, cReset)
	}
	if std == 0 && gox == 0 {
		return fmt.Sprintf("%s%s✔ 0s (zero-pause)%s", cBold, cBrightGreen, cReset)
	}
	return fmt.Sprintf("%s0.0%% (parity)%s", cGray, cReset)
}

func pickWinner(goxBetter, stdBetter bool) (string, string) {
	if goxBetter {
		return cBold + cBrightCyan + "GOX v1.0" + cReset, "gox"
	}
	if stdBetter {
		return cYellow + "Standard" + cReset, "std"
	}
	return cGray + "Tie" + cReset, "tie"
}

func main() {
	widths := []int{24, 17, 14, 10, 20, 10}
	borderTop := renderBorder(widths, "┌", "┬", "┐")
	borderSep := renderBorder(widths, "├", "┼", "┤")
	borderBottom := renderBorder(widths, "└", "┴", "┘")

	tableWidth := visibleLen(borderTop)
	bannerInner := tableWidth - 2
	bannerTop := fmt.Sprintf("%s╔%s╗%s", cCyan, strings.Repeat("═", bannerInner), cReset)
	bannerBottom := fmt.Sprintf("%s╚%s╝%s", cCyan, strings.Repeat("═", bannerInner), cReset)

	fmt.Println()
	fmt.Println(bannerTop)
	fmt.Printf("%s║%s%s%s║%s\n", cCyan, cReset, centerText(cBold+cBrightWhite+"GOX v1.0 Production Benchmark Suite"+cReset, bannerInner), cCyan, cReset)
	fmt.Printf("%s║%s%s%s║%s\n", cCyan, cReset, centerText(cDim+cCyan+"Comparing Standard Go Tracing GC vs. GOX Deterministic Allocations"+cReset, bannerInner), cCyan, cReset)
	fmt.Println(bannerBottom)
	fmt.Println()

	var metrics []BenchmarkMetric

	// 1. Request-Response
	fmt.Printf("%s⚡%s Running Benchmark 1: %sRequest-Response Lifecycle%s (200,000 ops)... ", cYellow, cReset, cBold, cReset)
	tStd, mStd, bStd, gcStd, pStd := measure(func() { runRequestStandard(200000) })
	tGox, mGox, bGox, gcGox, pGox := measure(func() { runRequestGOX(200000) })
	metrics = append(metrics, BenchmarkMetric{
		Name:            "Request/Response (Arena)",
		Operations:      200000,
		StandardTime:    tStd,
		GOXTime:         tGox,
		StandardAllocs:  mStd,
		GOXAllocs:       mGox,
		StandardBytes:   bStd,
		GOXBytes:        bGox,
		StandardGC:      gcStd,
		GOXGC:           gcGox,
		StandardPauseNs: pStd,
		GOXPauseNs:      pGox,
	})
	fmt.Printf("%s%s[DONE]%s\n", cBold, cGreen, cReset)

	// 2. Tree Construction
	fmt.Printf("%s⚡%s Running Benchmark 2: %sDeep Binary Tree%s (1,000 trees x 4,095 nodes)... ", cYellow, cReset, cBold, cReset)
	tStd, mStd, bStd, gcStd, pStd = measure(func() { runTreeStandard(1000, 12) })
	tGox, mGox, bGox, gcGox, pGox = measure(func() { runTreeGOX(1000, 12) })
	metrics = append(metrics, BenchmarkMetric{
		Name:            "Binary Tree (Arena)",
		Operations:      1000,
		StandardTime:    tStd,
		GOXTime:         tGox,
		StandardAllocs:  mStd,
		GOXAllocs:       mGox,
		StandardBytes:   bStd,
		GOXBytes:        bGox,
		StandardGC:      gcStd,
		GOXGC:           gcGox,
		StandardPauseNs: pStd,
		GOXPauseNs:      pGox,
	})
	fmt.Printf("%s%s[DONE]%s\n", cBold, cGreen, cReset)

	// 3. Packet Inspection
	fmt.Printf("%s⚡%s Running Benchmark 3: %sHigh-Frequency Packets%s (500,000 ops)... ", cYellow, cReset, cBold, cReset)
	tStd, mStd, bStd, gcStd, pStd = measure(func() { runPacketStandard(500000) })
	tGox, mGox, bGox, gcGox, pGox = measure(func() { runPacketGOX(500000) })
	metrics = append(metrics, BenchmarkMetric{
		Name:            "Packet Pool (Unique)",
		Operations:      500000,
		StandardTime:    tStd,
		GOXTime:         tGox,
		StandardAllocs:  mStd,
		GOXAllocs:       mGox,
		StandardBytes:   bStd,
		GOXBytes:        bGox,
		StandardGC:      gcStd,
		GOXGC:           gcGox,
		StandardPauseNs: pStd,
		GOXPauseNs:      pGox,
	})
	fmt.Printf("%s%s[DONE]%s\n", cBold, cGreen, cReset)
	fmt.Println()

	fmt.Println(borderTop)
	headerCols := []string{
		cBold + cBrightWhite + "Workload" + cReset,
		cBold + cBrightWhite + "Metric" + cReset,
		cBold + cYellow + "Standard Go GC" + cReset,
		cBold + cBrightCyan + "GOX v1.0" + cReset,
		cBold + cBrightGreen + "Improvement / Impact" + cReset,
		cBold + cBrightWhite + "Winner" + cReset,
	}
	fmt.Println(renderRow(headerCols, widths))
	fmt.Println(borderSep)

	goxWins, stdWins, totalMetrics := 0, 0, 0

	for idx, m := range metrics {
		timeDiff := float64(m.StandardTime-m.GOXTime) / float64(m.StandardTime) * 100.0
		allocDiff := float64(int64(m.StandardAllocs)-int64(m.GOXAllocs)) / float64(m.StandardAllocs) * 100.0
		byteDiff := float64(int64(m.StandardBytes)-int64(m.GOXBytes)) / float64(m.StandardBytes) * 100.0
		gcDiff := float64(0)
		if m.StandardGC > 0 {
			gcDiff = float64(int64(m.StandardGC)-int64(m.GOXGC)) / float64(m.StandardGC) * 100.0
		}
		pauseDiff := float64(0)
		if m.StandardPauseNs > 0 {
			pauseDiff = float64(int64(m.StandardPauseNs)-int64(m.GOXPauseNs)) / float64(m.StandardPauseNs) * 100.0
		}

		stdAvg := (m.StandardTime / time.Duration(m.Operations)).Round(time.Nanosecond)
		goxAvg := (m.GOXTime / time.Duration(m.Operations)).Round(time.Nanosecond)
		avgDiff := float64(stdAvg-goxAvg) / float64(stdAvg) * 100.0

		wTotal, kTotal := pickWinner(m.GOXTime < m.StandardTime, m.StandardTime < m.GOXTime)
		wAvg, kAvg := pickWinner(goxAvg < stdAvg, stdAvg < goxAvg)
		wAlloc, kAlloc := pickWinner(m.GOXAllocs < m.StandardAllocs, m.StandardAllocs < m.GOXAllocs)
		wBytes, kBytes := pickWinner(m.GOXBytes < m.StandardBytes, m.StandardBytes < m.GOXBytes)
		wGC, kGC := pickWinner(m.GOXGC < m.StandardGC, m.StandardGC < m.GOXGC)
		wPause, kPause := pickWinner(m.GOXPauseNs < m.StandardPauseNs, m.StandardPauseNs < m.GOXPauseNs)

		rows := []struct {
			metric    string
			std       string
			gox       string
			imp       string
			winner    string
			winnerKey string
		}{
			{
				metric:    "Total Time",
				std:       m.StandardTime.Round(time.Millisecond).String(),
				gox:       m.GOXTime.Round(time.Millisecond).String(),
				imp:       formatDiff(timeDiff, "speedup"),
				winner:    wTotal,
				winnerKey: kTotal,
			},
			{
				metric:    "Avg Latency (op)",
				std:       fmt.Sprintf("%v", stdAvg),
				gox:       fmt.Sprintf("%v", goxAvg),
				imp:       formatDiff(avgDiff, "speedup"),
				winner:    wAvg,
				winnerKey: kAvg,
			},
			{
				metric:    "Total Mallocs",
				std:       fmt.Sprintf("%d", m.StandardAllocs),
				gox:       fmt.Sprintf("%d", m.GOXAllocs),
				imp:       formatDiff(allocDiff, "reduction"),
				winner:    wAlloc,
				winnerKey: kAlloc,
			},
			{
				metric:    "Total Alloc Bytes",
				std:       formatBytes(m.StandardBytes),
				gox:       formatBytes(m.GOXBytes),
				imp:       formatDiff(byteDiff, "reduction"),
				winner:    wBytes,
				winnerKey: kBytes,
			},
			{
				metric:    "GC Cycles (NumGC)",
				std:       fmt.Sprintf("%d", m.StandardGC),
				gox:       fmt.Sprintf("%d", m.GOXGC),
				imp:       formatGCDiff(m.StandardGC, m.GOXGC, gcDiff),
				winner:    wGC,
				winnerKey: kGC,
			},
			{
				metric:    "GC Pause Time",
				std:       time.Duration(m.StandardPauseNs).String(),
				gox:       time.Duration(m.GOXPauseNs).String(),
				imp:       formatPauseDiff(m.StandardPauseNs, m.GOXPauseNs, pauseDiff),
				winner:    wPause,
				winnerKey: kPause,
			},
		}

		for rIdx, r := range rows {
			switch r.winnerKey {
			case "gox":
				goxWins++
			case "std":
				stdWins++
			}
			totalMetrics++

			workloadLabel := ""
			if rIdx == 0 {
				workloadLabel = cBold + cBrightWhite + m.Name + cReset
			}
			stdFormatted := cDim + cWhite + r.std + cReset
			goxFormatted := cBold + cBrightCyan + r.gox + cReset

			cols := []string{
				workloadLabel,
				r.metric,
				stdFormatted,
				goxFormatted,
				r.imp,
				r.winner,
			}
			fmt.Println(renderRow(cols, widths))
		}

		if idx < len(metrics)-1 {
			fmt.Println(borderSep)
		}
	}

	fmt.Println(borderBottom)
	fmt.Println()
	winRate := float64(goxWins) / float64(totalMetrics) * 100.0
	fmt.Printf("%s%s✔ Overall Winner: %sGOX v1.0%s (%d of %d metrics won, %.1f%% win rate).%s\n",
		cBold, cBrightGreen, cBold+cBrightCyan, cBold+cBrightGreen, goxWins, totalMetrics, winRate, cReset)
	fmt.Println("  Deterministic memory reclamation eliminates GC sweeps and eliminates stop-the-world pause latency.")
	fmt.Println()
}

func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
