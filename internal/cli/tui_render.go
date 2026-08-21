package cli

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shaktsin/biewer/internal/model"
)

type tuiView struct {
	Snapshot       model.Snapshot
	SessionHistory map[string][]tuiSample
	Selected       int
	Width          int
	Height         int
	Now            time.Time
	LastRefresh    time.Time
	Connected      bool
	Color          bool
}

type tuiSample struct {
	At     time.Time
	CPU    float64
	Memory uint64
	Tokens uint64
}

const (
	tuiReset     = "\033[0m"
	tuiBold      = "\033[1m"
	tuiBlue      = "\033[38;5;75m"
	tuiCyan      = "\033[38;5;45m"
	tuiGreen     = "\033[38;5;48m"
	tuiOrange    = "\033[38;5;208m"
	tuiPurple    = "\033[38;5;141m"
	tuiYellow    = "\033[38;5;220m"
	tuiRed       = "\033[38;5;203m"
	tuiMuted     = "\033[38;5;245m"
	tuiHeader    = "\033[38;5;231m\033[48;5;17m"
	tuiSelected  = "\033[38;5;231m\033[48;5;25m"
	tuiFooter    = "\033[38;5;252m\033[48;5;236m"
	tuiPanelName = "\033[38;5;117m\033[1m"
	tuiBorder    = "\033[38;5;33m"
)

type tuiLine struct {
	text  string
	style string
	raw   bool
}

// renderTUIFrame wraps a rendered view in a scroll-proof terminal update.
// Absolute cursor addressing is used for every row instead of newlines, so a
// refresh cannot advance the terminal viewport even after a resize or after
// painting the bottom-right cell. The whole frame is emitted with one stdout
// write by cmdTUI to minimize visible partial redraws.
func renderTUIFrame(view tuiView) string {
	rows := strings.Split(renderTUIView(view), "\r\n")
	var frame strings.Builder
	frame.Grow(len(rows)*(maxInt(1, view.Width)+12) + 24)
	frame.WriteString("\033[?7l\033[2J")
	for row, content := range rows {
		fmt.Fprintf(&frame, "\033[%d;1H", row+1)
		frame.WriteString(content)
	}
	frame.WriteString(tuiReset)
	return frame.String()
}

func renderTUIView(view tuiView) string {
	width := maxInt(44, view.Width)
	height := maxInt(12, view.Height)
	lines := make([]tuiLine, 0, height)

	status := "● LIVE"
	if !view.Connected {
		status = "● OFFLINE"
	}
	headerRight := fmt.Sprintf("%s   %s  ", status, view.Now.Format("15:04:05"))
	lines = append(lines, tuiLine{text: joinEdges("  BIEWER  /  agent monitor", headerRight, width), style: tuiHeader + tuiBold})

	if !view.Connected {
		lines = append(lines, tuiLine{text: fit("  Daemon unavailable · run `biewer enable`, then press r to reconnect", width), style: tuiRed})
		lines = append(lines, tuiLine{text: strings.Repeat("─", width), style: tuiMuted})
	} else {
		lines = append(lines, tuiLine{text: fit(tuiSummary(view.Snapshot), width), style: tuiCyan + tuiBold})
		lines = append(lines, tuiLine{text: strings.Repeat("─", width), style: tuiMuted})
	}

	bodyHeight := maxInt(1, height-len(lines)-1)
	leftWidth := maxInt(24, minInt(44, width/3))
	if width-leftWidth-1 < 18 {
		leftWidth = maxInt(20, width-19)
	}
	rightWidth := width - leftWidth - 1
	left := buildSessionPane(view, leftWidth, bodyHeight)
	right := buildDetailPane(view, rightWidth, bodyHeight)
	for row := 0; row < bodyHeight; row++ {
		lines = append(lines, combinePaneLine(left[row], right[row], leftWidth, rightWidth, view.Color))
	}

	footer := "  ↑/k up   ↓/j down   g/G first/last   r refresh   q quit"
	refreshAge := humanDuration(view.Now.Sub(view.LastRefresh))
	footer = joinEdges(footer, "updated "+refreshAge+" ago  ", width)
	lines = append(lines, tuiLine{text: fit(footer, width), style: tuiFooter})

	var output strings.Builder
	for index, line := range lines {
		if line.raw {
			output.WriteString(line.text)
		} else {
			if view.Color && line.style != "" {
				output.WriteString(line.style)
			}
			output.WriteString(fit(line.text, width))
			if view.Color && line.style != "" {
				output.WriteString(tuiReset)
			}
		}
		output.WriteString("\033[K")
		// A newline after the terminal's final row scrolls the entire screen
		// by one line on every refresh. Separate rows, but leave the cursor on
		// the footer so repeated frames remain fixed in place.
		if index < len(lines)-1 {
			output.WriteString("\r\n")
		}
	}
	return output.String()
}

func buildSessionPane(view tuiView, width, height int) []tuiLine {
	lines := make([]tuiLine, 0, height)
	lines = append(lines, tuiLine{
		text:  joinEdges("  SESSIONS", fmt.Sprintf("%d  ", len(view.Snapshot.Sessions)), width),
		style: tuiPanelName,
	})
	lines = append(lines, tuiLine{text: fit("  TASK / RESOURCE", width), style: tuiMuted})

	if len(view.Snapshot.Sessions) == 0 {
		lines = append(lines,
			tuiLine{text: "  No sessions detected", style: tuiBold},
			tuiLine{text: "  Start Claude or Codex", style: tuiMuted},
		)
		return padTUILines(lines, width, height)
	}

	visible := maxInt(1, (height-2)/2)
	start := 0
	if view.Selected >= visible {
		start = view.Selected - visible + 1
	}
	if start+visible > len(view.Snapshot.Sessions) {
		start = maxInt(0, len(view.Snapshot.Sessions)-visible)
	}
	for index := start; index < start+visible && index < len(view.Snapshot.Sessions); index++ {
		session := view.Snapshot.Sessions[index]
		marker := "  "
		style := agentTUIStyle(session.Session.Agent)
		if index == view.Selected {
			marker = "▶ "
			style = tuiSelected + tuiBold
		}
		project := truncateRunes(session.Session.Project, maxInt(4, width-15))
		lines = append(lines, tuiLine{
			text:  marker + project,
			style: style,
		})
		secondary := ""
		switch session.ResourceScope {
		case model.ResourceShared:
			secondary = fmt.Sprintf("  ◆ shared · %d processes", session.ProcessCount)
		case model.ResourceNone:
			secondary = fmt.Sprintf("  ○ %s · %s · %s ago", shortAgent(session.Session.Agent), humanTokens(session.Usage.TotalTokens), humanDuration(view.Now.Sub(session.Session.LastActivityAt)))
		default:
			statusMark := "●"
			if sessionAttribution(session) == model.AttributionProbable {
				statusMark = "◌"
			}
			secondary = fmt.Sprintf("  %s %s · %s", statusMark, shortAgent(session.Session.Agent), shortID(session.Session.ID))
			if session.Usage.TotalTokens > 0 {
				secondary = fmt.Sprintf("  %s %s · %s", statusMark, shortAgent(session.Session.Agent), humanTokens(session.Usage.TotalTokens))
			}
		}
		lines = append(lines, tuiLine{
			text:  secondary,
			style: style,
		})
	}
	return padTUILines(lines, width, height)
}

func buildDetailPane(view tuiView, width, height int) []tuiLine {
	lines := make([]tuiLine, 0, height)
	appendLine := func(text, style string) {
		if len(lines) < height {
			lines = append(lines, tuiLine{text: fit(text, width), style: style})
		}
	}

	if len(view.Snapshot.Sessions) == 0 {
		appendLine("  SESSION DETAILS", tuiPanelName)
		appendLine("", "")
		appendLine("  Select a running coding agent", tuiBold)
		appendLine("  Details, token usage, findings, and", tuiMuted)
		appendLine("  its process tree will appear here.", tuiMuted)
		return padTUILines(lines, width, height)
	}

	index := minInt(maxInt(0, view.Selected), len(view.Snapshot.Sessions)-1)
	session := view.Snapshot.Sessions[index]
	appendLine(joinEdges("  "+session.Session.Project, shortID(session.Session.ID)+"  ", width), agentTUIStyle(session.Session.Agent)+tuiBold)
	agentModel := string(session.Session.Agent)
	if session.Session.Model != "" {
		agentModel += " / " + session.Session.Model
	}
	sourceLabel := string(session.Session.Source)
	if session.ResourceScope == model.ResourceShared {
		sourceLabel = "shared desktop resources"
	}
	appendLine("  "+agentModel+"  ·  "+sourceLabel+"  ·  "+strings.ToUpper(string(sessionAttribution(session))), tuiMuted)
	if session.Session.Cwd != "" {
		appendLine("  "+truncateRunes(session.Session.Cwd, maxInt(1, width-2)), tuiMuted)
	}
	appendLine("", "")

	showOverview := width >= 52 && height >= 15
	if showOverview {
		for _, line := range buildSelectedOverview(view, session, width) {
			appendLine(line.text, line.style)
		}
		appendLine("", "")
	}

	if session.Usage.TotalTokens > 0 && !showOverview {
		appendLine("  TOKEN USAGE", tuiPurple+tuiBold)
		appendLine(fmt.Sprintf("  TOTAL %-9s INPUT %-9s CACHED %s", humanTokens(session.Usage.TotalTokens), humanTokens(session.Usage.InputTokens), humanTokens(session.Usage.CachedInputTokens)), "")
		appendLine(fmt.Sprintf("  OUTPUT %-8s REASONING %-7s CACHE WRITE %s", humanTokens(session.Usage.OutputTokens), humanTokens(session.Usage.ReasoningTokens), humanTokens(session.Usage.CacheWriteTokens)), tuiMuted)
		appendLine("", "")
	}
	if hasAgentMetrics(session.Metrics) {
		appendLine("  AGENT METRICS", tuiOrange+tuiBold)
		appendLine(fmt.Sprintf("  COST $%.4f   ACTIVE %s", session.Metrics.CostUSD, humanDuration(time.Duration(session.Metrics.ActiveSeconds*float64(time.Second)))), "")
		appendLine(fmt.Sprintf("  LINES +%d/-%d   COMMITS %d   PRS %d", session.Metrics.LinesAdded, session.Metrics.LinesRemoved, session.Metrics.Commits, session.Metrics.PullRequests), tuiMuted)
		appendLine("", "")
	}

	switch session.ResourceScope {
	case model.ResourceNone:
		appendLine("  ◇ DESKTOP-SHARED RESOURCES", tuiPurple+tuiBold)
		appendLine("  Task tokens are exact · CPU/MEM stay in shared row", tuiMuted)
	case model.ResourceShared:
		appendLine("  ◆ SHARED RESOURCES", tuiPurple+tuiBold)
		appendLine(fmt.Sprintf("  CPU %.1f%%   MEM %s   %d PROCESSES", session.CPUSeconds, humanBytes(session.MemoryBytes), session.ProcessCount), "")
		appendLine("  Combined desktop totals; not assigned per task.", tuiMuted)
	default:
		resourceMark := "●"
		if sessionAttribution(session) == model.AttributionProbable {
			resourceMark = "◌"
		}
		appendLine("  "+resourceMark+" RESOURCES · "+strings.ToUpper(string(sessionAttribution(session))), tuiGreen+tuiBold)
		appendLine(fmt.Sprintf("  CPU %.1f%%   MEM %s   %d PROCESSES", session.CPUSeconds, humanBytes(session.MemoryBytes), session.ProcessCount), "")
	}
	appendLine("", "")

	if len(session.Findings) > 0 {
		appendLine("  FINDINGS", tuiOrange+tuiBold)
		for _, finding := range session.Findings {
			style := tuiCyan
			if finding.Severity == model.SeverityWarn {
				style = tuiYellow
			}
			appendLine(fmt.Sprintf("  %s  %s", finding.Severity, finding.Message), style+tuiBold)
		}
		appendLine("", "")
	}

	if session.ResourceScope == model.ResourceNone {
		return padTUILines(lines, width, height)
	}

	appendLine("  PROCESS TREE", tuiBlue+tuiBold)
	processLines := make([]string, 0, session.ProcessCount)
	for _, process := range session.ProcessTree {
		appendTUIProcessLines(&processLines, process, "  ", width)
	}
	if len(processLines) == 0 {
		processLines = append(processLines, "  No attributed processes")
	}
	for _, processLine := range processLines {
		if len(lines) >= height {
			break
		}
		appendLine(processLine, "")
	}
	return padTUILines(lines, width, height)
}

func buildSelectedOverview(view tuiView, session model.SessionSnapshot, width int) []tuiLine {
	history := view.SessionHistory[tuiSessionKey(session)]
	if len(history) == 0 {
		history = []tuiSample{sessionTUISample(session, view.Now)}
	}

	memScale := uint64(8 * 1024 * 1024 * 1024)
	if session.MemoryBytes > memScale {
		memScale = session.MemoryBytes
	}
	resourceLine := " CPU/MEM  desktop shared · not assigned to this task"
	if session.ResourceScope != model.ResourceNone {
		barWidth := maxInt(4, minInt(8, (width-38)/2))
		resourceLine = fmt.Sprintf(" CPU %5.1f%% %s  MEM %7s %s · %d procs",
			session.CPUSeconds,
			progressBar(session.CPUSeconds, 100, barWidth),
			humanBytes(session.MemoryBytes),
			progressBar(float64(session.MemoryBytes), float64(memScale), barWidth),
			session.ProcessCount,
		)
	}
	tokenLine := fmt.Sprintf(" TOKENS %7s · IN %7s · CACHE %7s · OUT %s",
		humanTokens(session.Usage.TotalTokens),
		humanTokens(session.Usage.InputTokens),
		humanTokens(session.Usage.CachedInputTokens),
		humanTokens(session.Usage.OutputTokens),
	)
	rate := latestTokenRate(history)
	sparkWidth := maxInt(6, minInt(12, (width-28)/2))
	activityLine := fmt.Sprintf(" ACT  CPU %s  TOK/m %-7s %s",
		sparkline(sampleCPU(history), sparkWidth),
		humanTokens(uint64(rate)),
		sparkline(sampleTokenRates(history), sparkWidth),
	)

	return []tuiLine{
		{text: overviewBorder("╭", "╮", "SESSION OVERVIEW", width), style: tuiBorder + tuiBold},
		{text: overviewContent(resourceLine, width), style: tuiCyan},
		{text: overviewContent(tokenLine, width), style: tuiPurple},
		{text: overviewContent(activityLine, width), style: tuiGreen},
		{text: overviewBorder("╰", "╯", "", width), style: tuiBorder},
	}
}

func overviewBorder(left, right, title string, width int) string {
	interior := maxInt(0, width-2)
	segment := strings.Repeat("─", interior)
	if title != "" {
		segment = fitWith("─ "+title+" ", "─", interior)
	}
	return left + segment + right
}

func overviewContent(text string, width int) string {
	return "│" + fit(text, maxInt(0, width-2)) + "│"
}

func combinePaneLine(left, right tuiLine, leftWidth, rightWidth int, color bool) tuiLine {
	var text strings.Builder
	text.WriteString(styledCell(left, leftWidth, color))
	if color {
		text.WriteString(tuiMuted)
	}
	text.WriteString("│")
	if color {
		text.WriteString(tuiReset)
	}
	text.WriteString(styledCell(right, rightWidth, color))
	return tuiLine{text: text.String(), raw: color}
}

func styledCell(line tuiLine, width int, color bool) string {
	text := fit(line.text, width)
	if !color || line.style == "" {
		return text
	}
	return line.style + text + tuiReset
}

func padTUILines(lines []tuiLine, width, height int) []tuiLine {
	if len(lines) > height {
		return lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, tuiLine{text: strings.Repeat(" ", width)})
	}
	return lines
}

func tuiSummary(snapshot model.Snapshot) string {
	stats := collectTUIStats(snapshot)
	return fmt.Sprintf("  %d TASKS   %d SHARED   %s TOKENS   CPU %.1f%%   MEM %s   %d PROCS   %d PORTS   %d FINDINGS",
		stats.tasks, stats.shared, humanTokens(stats.totalTokens), stats.cpu, humanBytes(stats.memory), stats.processes, stats.ports, stats.findings)
}

type tuiStats struct {
	cpu, cost                                 float64
	memory                                    uint64
	input, cached, cacheWrite, output         uint64
	reasoning, totalTokens                    uint64
	processes, ports, findings, tasks, shared int
}

func collectTUIStats(snapshot model.Snapshot) tuiStats {
	var stats tuiStats
	for _, session := range snapshot.Sessions {
		stats.cpu += session.CPUSeconds
		stats.memory += session.MemoryBytes
		stats.processes += session.ProcessCount
		stats.ports += len(session.ListenPorts)
		stats.findings += len(session.Findings)
		stats.input += session.Usage.InputTokens
		stats.cached += session.Usage.CachedInputTokens
		stats.cacheWrite += session.Usage.CacheWriteTokens
		stats.output += session.Usage.OutputTokens
		stats.reasoning += session.Usage.ReasoningTokens
		stats.totalTokens += session.Usage.TotalTokens
		stats.cost += session.Metrics.CostUSD
		if session.ResourceScope == model.ResourceShared {
			stats.shared++
		} else {
			stats.tasks++
		}
	}
	return stats
}

func sessionTUISample(session model.SessionSnapshot, at time.Time) tuiSample {
	return tuiSample{At: at, CPU: session.CPUSeconds, Memory: session.MemoryBytes, Tokens: session.Usage.TotalTokens}
}

func appendTUISample(history []tuiSample, sample tuiSample, limit int) []tuiSample {
	if len(history) > 0 && !sample.At.After(history[len(history)-1].At) {
		history[len(history)-1] = sample
		return history
	}
	history = append(history, sample)
	if len(history) > limit {
		history = append([]tuiSample(nil), history[len(history)-limit:]...)
	}
	return history
}

func sampleCPU(history []tuiSample) []float64 {
	values := make([]float64, len(history))
	for index, sample := range history {
		values[index] = sample.CPU
	}
	return values
}

func sampleTokenRates(history []tuiSample) []float64 {
	if len(history) < 2 {
		return []float64{0}
	}
	rates := make([]float64, 0, len(history)-1)
	for index := 1; index < len(history); index++ {
		elapsed := history[index].At.Sub(history[index-1].At).Minutes()
		var rate float64
		if elapsed > 0 && history[index].Tokens >= history[index-1].Tokens {
			rate = float64(history[index].Tokens-history[index-1].Tokens) / elapsed
		}
		rates = append(rates, rate)
	}
	return rates
}

func latestTokenRate(history []tuiSample) float64 {
	rates := sampleTokenRates(history)
	if len(rates) == 0 {
		return 0
	}
	return rates[len(rates)-1]
}

func sparkline(values []float64, width int) string {
	if width <= 0 {
		return ""
	}
	if len(values) == 0 {
		return strings.Repeat("▁", width)
	}
	if len(values) > width {
		values = values[len(values)-width:]
	}
	var maxValue float64
	for _, value := range values {
		if value > maxValue {
			maxValue = value
		}
	}
	levels := []rune("▁▂▃▄▅▆▇█")
	var text strings.Builder
	text.WriteString(strings.Repeat("▁", width-len(values)))
	for _, value := range values {
		level := 0
		if maxValue > 0 {
			level = int((value / maxValue) * float64(len(levels)-1))
		}
		level = minInt(maxInt(0, level), len(levels)-1)
		text.WriteRune(levels[level])
	}
	return text.String()
}

func progressBar(value, maximum float64, width int) string {
	if width <= 0 {
		return ""
	}
	ratio := 0.0
	if maximum > 0 {
		ratio = value / maximum
	}
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	filled := int(ratio*float64(width) + 0.5)
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

func storageLabel(storage string) string {
	if storage == "" {
		return "local db"
	}
	return storage
}

func maxUint64(values ...uint64) uint64 {
	var maximum uint64
	for _, value := range values {
		if value > maximum {
			maximum = value
		}
	}
	return maximum
}

func humanTokens(tokens uint64) string {
	switch {
	case tokens >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(tokens)/1_000_000_000)
	case tokens >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(tokens)/1_000_000)
	case tokens >= 1_000:
		return fmt.Sprintf("%.1fK", float64(tokens)/1_000)
	default:
		return fmt.Sprintf("%d", tokens)
	}
}

func hasAgentMetrics(metrics model.AgentMetrics) bool {
	return metrics.CostUSD != 0 || metrics.ActiveSeconds != 0 || metrics.LinesAdded != 0 || metrics.LinesRemoved != 0 || metrics.Commits != 0 || metrics.PullRequests != 0
}

func agentTUIStyle(agent model.Agent) string {
	switch agent {
	case model.AgentClaude, model.AgentClaudeDesktop:
		return tuiOrange
	case model.AgentCodex, model.AgentCodexDesktop:
		return tuiBlue
	case model.AgentChatGPT:
		return tuiGreen
	default:
		return tuiCyan
	}
}

func sessionAttribution(session model.SessionSnapshot) model.AttributionConfidence {
	if session.Attribution != "" {
		return session.Attribution
	}
	switch session.ResourceScope {
	case model.ResourceShared:
		return model.AttributionShared
	case model.ResourceNone:
		return model.AttributionNone
	default:
		if session.Session.Source == model.SourceAuto {
			return model.AttributionProbable
		}
		return model.AttributionConfirmed
	}
}

func shortAgent(agent model.Agent) string {
	switch agent {
	case model.AgentClaudeDesktop:
		return "claude desktop"
	case model.AgentCodexDesktop:
		return "codex desktop"
	default:
		return string(agent)
	}
}

func appendTUIProcessLines(lines *[]string, process *model.Process, indent string, width int) {
	branch := "└─"
	if process.Depth == 0 {
		branch = "●"
	}
	left := fmt.Sprintf("%s%s %s", indent, branch, process.Command)
	right := fmt.Sprintf("%s  %5.1f%%", humanBytes(process.RSSBytes), process.CPUPct)
	if len(process.Ports) > 0 {
		right += "  " + shortPorts(process.Ports)
	}
	*lines = append(*lines, fit(joinEdges(left, right, width), width))
	for _, child := range process.Children {
		appendTUIProcessLines(lines, child, indent+"  ", width)
	}
}

func joinEdges(left, right string, width int) string {
	space := width - utf8.RuneCountInString(left) - utf8.RuneCountInString(right)
	if space < 1 {
		return fit(left, maxInt(1, width-utf8.RuneCountInString(right)-1)) + " " + right
	}
	return left + strings.Repeat(" ", space) + right
}

func fit(text string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) > width {
		if width == 1 {
			return "…"
		}
		return string(runes[:width-1]) + "…"
	}
	return text + strings.Repeat(" ", width-len(runes))
}

func fitWith(text, fill string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) >= width {
		return string(runes[:width])
	}
	return text + strings.Repeat(fill, width-len(runes))
}

func truncateRunes(text string, width int) string {
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	if width <= 1 {
		return string(runes[:width])
	}
	return string(runes[:width-1]) + "…"
}
