package tui

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/mjacobs/llmtop/internal/model"
)

// HostSpec describes one host the App tracks: its update-routing name plus
// the display-only settings that came from its config entry.
type HostSpec struct {
	Name string // routing key: config name, or the --connect address
	Addr string // remote address ("" for local); struck from error text when addresses are hidden
	GPUs []int  // GPU indices to show, in order; nil = all in device order
	// NameFromAgent marks a --connect host. It has no configured name, so
	// its title becomes the name its agent reports once a snapshot arrives.
	NameFromAgent bool
}

// ViewConfig carries the config file's display-only settings into the TUI.
// Everything here shapes rendering; nothing here changes collection.
type ViewConfig struct {
	Hosts             []HostSpec
	ShowAddresses     bool
	ShowHostnames     bool
	ShowGraphicsProcs bool
	// Layout is the [layout] section's preferred arrangement. The zero
	// value is the built-in heuristic.
	Layout LayoutPref
}

// hostView is a HostSpec resolved against the global toggles, held by each
// HostState and applied to every snapshot as it arrives (filterSnapshot),
// so the solver and renderers only ever see the filtered data and their
// line accounting stays untouched.
type hostView struct {
	gpus          []int // nil = all
	showGraphics  bool
	showAddrs     bool
	showNames     bool
	nameFromAgent bool     // title with the agent's reported name (--connect hosts)
	placeholder   string   // "hostN": the title when the real name can't be shown
	redact        []string // address strings struck from displayed errors
}

// passthrough reports whether the view changes nothing, letting Apply keep
// the snapshot as delivered.
func (v hostView) passthrough() bool {
	return v.gpus == nil && v.showGraphics && v.showAddrs
}

// filterSnapshot applies the view to one snapshot, returning s itself when
// nothing filters. GPU selection drops the hidden GPUs' processes too, so a
// server running only on hidden GPUs falls through mapServersToGPUs to the
// host strip instead of vanishing. Hiding graphics processes only trims the
// process table: the mem chart's total (mauve) series reads GPU.MemUsed,
// which still includes their VRAM. Hiding addresses redacts server
// endpoints to ":port" — GenTPSHist is keyed by the redacted endpoint from
// the first sample on, so history stays consistent — and passes the
// displayed free-form strings through scrubErr within scrubBudget.
func (v hostView) filterSnapshot(s *model.Snapshot) *model.Snapshot {
	if v.passthrough() {
		return s
	}
	out := *s
	if v.gpus != nil {
		out.GPUs = make([]model.GPU, 0, len(v.gpus))
		for _, want := range v.gpus {
			for _, g := range s.GPUs {
				if g.Index == want {
					out.GPUs = append(out.GPUs, g)
					break
				}
			}
		}
		visible := make(map[int]bool, len(out.GPUs))
		for _, g := range out.GPUs {
			visible[g.Index] = true
		}
		out.Procs = filterProcs(out.Procs, func(p model.GPUProc) bool { return visible[p.GPUIndex] })
	}
	if !v.showGraphics {
		out.Procs = filterProcs(out.Procs, func(p model.GPUProc) bool { return p.Class != model.ProcGraphics })
	}
	if !v.showAddrs {
		sc := snapScrubber{v: v, left: scrubBudget}
		out.Errors = sc.errorLines(out.Errors)
		out.Host.DriverVersion = sc.text(out.Host.DriverVersion)
		out.GPUs = append([]model.GPU(nil), out.GPUs...)
		for i := range out.GPUs {
			out.GPUs[i].Name = sc.text(out.GPUs[i].Name)
		}
		out.Procs = append([]model.GPUProc(nil), out.Procs...)
		for i := range out.Procs {
			out.Procs[i].Name = sc.text(out.Procs[i].Name)
		}
		servers := make([]model.LLMServer, len(out.Servers))
		copy(servers, out.Servers)
		for i := range servers {
			srv := &servers[i]
			srv.Endpoint = redactEndpoint(srv.Endpoint)
			srv.Model = sc.text(srv.Model)
			switch srv.Kind {
			case "llama.cpp", "vllm", "ninfer":
				// Known engine identifiers select metrics advice. Keep
				// their meaning, like known statuses and the router role;
				// only free-form kinds can contain an address.
			default:
				srv.Kind = sc.text(srv.Kind)
			}
			srv.MetricsErr = sc.text(srv.MetricsErr)
			if !knownStatus[srv.Status] {
				srv.Status = sc.text(srv.Status)
			}
			if srv.Role != "router" {
				srv.Role = sc.text(srv.Role)
			}
		}
		out.Servers = servers
	}
	return &out
}

// knownStatus lists the server states the collector sets; they hold no
// address and skip the scrub.
var knownStatus = map[string]bool{
	"": true, model.StatusOK: true, model.StatusLoading: true,
	model.StatusUnreachable: true, model.StatusSleeping: true,
}

// scrubBudget bounds, per snapshot, the bytes of text scrubErr examines
// while addresses are hidden, and maxErrorLines the collection-error lines
// kept. A remote agent decides how many errors and servers a snapshot
// holds (up to the client's 8 MB body limit), and the scrub runs on the
// TUI's update loop, so the work for one snapshot must not grow with it.
// Each string scrubbed costs at most maxScrubLen bytes of the budget; past
// the budget a string is not scrubbed and not shown. At the worst measured
// cost, about 130 ns a byte, the budget is about 5 ms per snapshot.
const (
	scrubBudget   = 32 << 10
	maxErrorLines = 8
)

// snapScrubber scrubs one snapshot's strings within scrubBudget.
type snapScrubber struct {
	v    hostView
	left int // bytes of budget left
}

// text returns msg scrubbed, or "…" when the budget is spent.
func (s *snapScrubber) text(msg string) string {
	if msg == "" {
		return ""
	}
	if s.left <= 0 {
		return "…"
	}
	s.left -= min(len(msg), maxScrubLen)
	return s.v.scrubErr(msg)
}

// errorLines scrubs a snapshot's collection errors, keeping at most
// maxErrorLines of them and only while the budget lasts; the lines left
// out are counted on one last line, "… N more errors", which shows that
// they exist without showing their text.
func (s *snapScrubber) errorLines(errs []string) []string {
	if len(errs) == 0 {
		return errs
	}
	out := make([]string, 0, min(len(errs), maxErrorLines)+1)
	for _, e := range errs {
		if len(out) == maxErrorLines || s.left <= 0 {
			break
		}
		out = append(out, s.text(e))
	}
	switch rest := len(errs) - len(out); {
	case rest == 1:
		out = append(out, "… 1 more error")
	case rest > 1:
		out = append(out, fmt.Sprintf("… %d more errors", rest))
	}
	return out
}

// filterProcs returns the processes keep admits, always in a fresh slice so
// the caller never mutates the snapshot it was handed.
func filterProcs(procs []model.GPUProc, keep func(model.GPUProc) bool) []model.GPUProc {
	out := make([]model.GPUProc, 0, len(procs))
	for _, p := range procs {
		if keep(p) {
			out = append(out, p)
		}
	}
	return out
}

// redactEndpoint strips the host from an endpoint URL, keeping the port so
// servers on one host stay distinguishable: "http://192.0.2.10:8080" →
// ":8080". Anything without a recognizable port redacts entirely.
func redactEndpoint(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil && u.Port() != "" {
		return ":" + u.Port()
	}
	return "…"
}

// scrubErr makes an error message safe for the banner while addresses are
// hidden. Dial and HTTP errors embed the address as configured (in the
// request URL) and, for a host given by name, the address it resolved to
// ("dial tcp 192.0.2.10:9832: connect: …"), so it does two things: it
// strikes the host's own redact strings, which covers a configured host
// name, and then replaces every IP address left anywhere in the text
// (scrubIPs). With addresses shown the message passes through unchanged.
// It serves the connection-error banner (HostState.ErrMsg) and, through
// snapScrubber, a snapshot's displayed free-form strings.
func (v hostView) scrubErr(msg string) string {
	if v.showAddrs {
		return msg
	}
	// Cut first, so striking the redact strings costs no more than the
	// scan, however long msg is. The cut leaves twice what scrubIPs keeps,
	// and drops a redact string it splits, so striking still sees every
	// one that could reach the kept text.
	msg = cutForRedact(msg, 2*maxScrubLen, v.redact)
	for _, r := range v.redact {
		msg = strings.ReplaceAll(msg, r, "…")
	}
	return scrubIPs(msg)
}

// maxAddrPiece bounds the pieces scrubIPs tests. The longest address it
// needs to see, a bracketed IPv6 with an IPv4 tail, a zone and a port, is
// about 70 bytes; the bound keeps the scan linear on long error text.
const maxAddrPiece = 96

// maxScrubLen caps the text scrubIPs examines. The scan is linear, at
// most about 130 ns a byte on the slowest shapes measured (runs such as
// "1:1:1:1:1:1:1:1x"), so about 0.5 ms for a capped text. An error is drawn
// on one screen line, truncated to the panel width, so nothing past the
// first few hundred bytes is ever seen; text past this cap is dropped and
// marked with "…". scrubBudget bounds the texts per snapshot.
const maxScrubLen = 4096

// capScrubInput cuts msg to at most maxScrubLen bytes, marking a cut with
// "…". A cut inside a run of address characters drops that whole run, so
// the start of an address split by the cut, which isIPForm would no
// longer recognise, is not shown; the cut also never splits a UTF-8
// sequence.
func capScrubInput(msg string) string {
	if len(msg) <= maxScrubLen {
		return msg
	}
	return msg[:safeCut(msg, maxScrubLen)] + "…"
}

// safeCut returns where to cut msg, at most n bytes (n < len(msg)): before
// the run of address characters the cut would split, and never inside a
// UTF-8 sequence.
func safeCut(msg string, n int) int {
	cut := n
	if isAddrRunByte(msg[cut]) {
		for cut > 0 && isAddrRunByte(msg[cut-1]) {
			cut--
		}
	}
	for cut > 0 && !utf8.RuneStart(msg[cut]) {
		cut--
	}
	return cut
}

// cutForRedact cuts msg to at most n bytes for scrubErr, marking a cut
// with "…". Besides what safeCut drops, it drops any tail of the kept text
// that begins one of the redact strings, such as "http://" left from a
// configured URL whose host the cut split, so no part of a redact string
// survives unstruck.
func cutForRedact(msg string, n int, redact []string) string {
	if len(msg) <= n {
		return msg
	}
	kept := msg[:safeCut(msg, n)]
	for trimmed := true; trimmed; {
		trimmed = false
		for _, r := range redact {
			for k := min(len(r)-1, len(kept)); k > 0; k-- {
				if strings.HasSuffix(kept, r[:k]) {
					kept = kept[:len(kept)-k]
					trimmed = true
					break
				}
			}
		}
	}
	return kept + "…"
}

// scrubIPs replaces each IP address in msg with "…". The rule: split msg
// into runs of address characters (letters, digits and . : % [ ] - _), so
// spaces, quotes, slashes, commas, parentheses, "=", "@" and ">" all end a
// run; then, inside each run, replace the leftmost longest piece that
// isIPForm accepts, again and again. A piece starts at the start of the run
// or just after a '-', '_', ':' or '.', and ends at the end of the run or
// just before one of them; it may not end in one of them either (a
// trailing '.', ':' or '-' is punctuation after the address, and isIPForm
// would take "192.0.2.1:9832-" for a port), except in "::". isIPForm
// strips brackets, a port and a zone before it parses, so one rule covers
// IPv4 with or without a port, bracketed or bare IPv6, IPv6 with a zone,
// IPv4-mapped IPv6, an address inside a URL or after "dial tcp", "on",
// "connect:" or "->", several addresses in one message, and an address
// beside punctuation or glued to a word by ':' or '-'. The port goes with
// the address it belongs to.
//
// It does not catch what isIPForm does not recognise: decimal, hex, octal
// and zero-padded IPv4 forms (3221225985, 0xC0000201), an address joined
// to letters or digits without a separator, name-shaped spellings such as
// ip-192-0-2-10 (whose dashes split it into short numbers), and
// percent-encoded brackets. Host names are not addresses here: only the
// host's own configured name is struck, by scrubErr. In the other
// direction, text that happens to be an IP literal, such as a bare
// 1.2.3.4 or "::", is replaced too.
//
// Only the first maxScrubLen bytes are examined and kept; the rest of a
// longer message is dropped (capScrubInput).
func scrubIPs(msg string) string {
	msg = capScrubInput(msg)
	var b strings.Builder
	for i := 0; i < len(msg); {
		j := i
		for j < len(msg) && isAddrRunByte(msg[j]) {
			j++
		}
		if j == i {
			b.WriteByte(msg[i])
			i++
			continue
		}
		b.WriteString(scrubRun(msg[i:j]))
		i = j
	}
	return b.String()
}

// isAddrRunByte reports whether c can appear in a run scrubIPs examines:
// the characters of an address with its brackets, port and zone.
func isAddrRunByte(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	}
	return strings.IndexByte(".:%[]-_", c) >= 0
}

// isPieceEdge reports whether c separates the pieces of a run: a piece
// starts just after one of these characters and can end just before one.
func isPieceEdge(c byte) bool {
	return c == '-' || c == '_' || c == ':' || c == '.'
}

// scrubRun applies scrubIPs's piece rule to one run of address characters.
func scrubRun(run string) string {
	var b strings.Builder
	last := 0 // run[last:] is not yet written
	for start := 0; start < len(run); {
		if end := longestAddrPiece(run, start); end > start {
			b.WriteString(run[last:start])
			b.WriteString("…")
			last, start = end, end
			continue
		}
		// Advance to the next piece start, just after an edge character.
		next := start
		for next < len(run) && !isPieceEdge(run[next]) {
			next++
		}
		start = next + 1
	}
	b.WriteString(run[last:])
	return b.String()
}

// longestAddrPiece returns the end of the longest piece of run beginning
// at start that isIPForm accepts, or start when there is none.
//
// Testing every possible end with isIPForm costs up to maxAddrPiece parses
// at every piece start, about 5 µs a byte on a run of "::". So it tests
// only the ends isIPForm could accept, longest first. For a piece made of
// run characters, isIPForm strips leading brackets, then parses a literal h
// made of hex digits, '.' and ':' that starts the rest of the piece; what
// may follow h is nothing, brackets, a '%' and a zone, "]:" and a port, or,
// when h is IPv4, ':' and a port (a port never holds ':', '[' or ']').
// addrPieceEnds gives those ends; isIPForm still decides each one, so the
// result is the same as testing every end.
func longestAddrPiece(run string, start int) int {
	ends := addrPieceEnds(run, start)
	for end := ends.max(); end > start; end-- {
		if !ends.has(end) {
			continue
		}
		if end < len(run) && !isPieceEdge(run[end]) {
			continue
		}
		piece := run[start:end]
		if isPieceEdge(piece[len(piece)-1]) && !strings.HasSuffix(piece, "::") {
			continue
		}
		if isIPForm(piece) {
			return end
		}
	}
	return start
}

// endRanges holds up to four ranges of piece ends, each lo+1 … hi.
type endRanges struct {
	n      int
	lo, hi [4]int
}

func (r *endRanges) add(lo, hi int) {
	if hi > lo {
		r.lo[r.n], r.hi[r.n] = lo, hi
		r.n++
	}
}

func (r *endRanges) max() int {
	m := 0
	for i := range r.n {
		m = max(m, r.hi[i])
	}
	return m
}

func (r *endRanges) has(e int) bool {
	for i := range r.n {
		if r.lo[i] < e && e <= r.hi[i] {
			return true
		}
	}
	return false
}

// maxIPText is the longest text net.ParseIP accepts.
const maxIPText = len("ffff:ffff:ffff:ffff:ffff:ffff:255.255.255.255")

// addrPieceEnds returns the ends of the pieces of run beginning at start
// that isIPForm could accept (see longestAddrPiece). Each range holds only
// ends past a literal that parses, so on text without addresses it is
// empty or short.
func addrPieceEnds(run string, start int) endRanges {
	var r endRanges
	lim := min(len(run), start+maxAddrPiece)
	q := start
	for q < lim && (run[q] == '[' || run[q] == ']') {
		q++
	}
	// A literal longer than maxIPText cannot parse, so the scan stops
	// there.
	hexEnd := q
	for hexEnd < lim && hexEnd <= q+maxIPText && isIPLiteralByte(run[hexEnd]) {
		hexEnd++
	}
	if hexEnd == q {
		return r
	}
	whole := hexEnd-q <= maxIPText // run[q:hexEnd] is the whole literal
	// portEnd is where a port starting at i must stop: at ':', '[' or ']'.
	portEnd := func(i int) int {
		for i < lim && run[i] != ':' && run[i] != '[' && run[i] != ']' {
			i++
		}
		return i
	}
	// A literal followed by '%' or a bracket.
	if whole && hexEnd < lim && strings.IndexByte("%[]", run[hexEnd]) >= 0 && net.ParseIP(run[q:hexEnd]) != nil {
		if run[hexEnd] == '%' {
			r.add(hexEnd, lim) // any zone, then anything
		} else {
			j := hexEnd
			for j < lim && (run[j] == '[' || run[j] == ']') {
				j++
			}
			if run[hexEnd] == ']' && j == hexEnd+1 && j < lim && run[j] == ':' {
				r.add(j+1, portEnd(j+1)) // "]:" and a port
			}
			r.add(hexEnd, j) // trailing brackets
		}
	}
	// An IPv4 literal followed by ':' and a port.
	if c := strings.IndexByte(run[q:hexEnd], ':'); c > 0 && net.ParseIP(run[q:q+c]) != nil {
		r.add(q+c+1, portEnd(q+c+1))
	}
	// The literal alone, at most as long as a viable IP prefix.
	r.add(q, q+viableIPPrefix(run[q:hexEnd]))
	return r
}

// isIPLiteralByte reports whether c can appear in a textual IP address.
func isIPLiteralByte(c byte) bool {
	switch {
	case '0' <= c && c <= '9', 'a' <= c && c <= 'f', 'A' <= c && c <= 'F':
		return true
	}
	return c == '.' || c == ':'
}

// viableIPPrefix returns the length of the longest prefix of s (hex
// digits, '.' and ':' only) that could still begin an IP address
// net.ParseIP accepts. Every rule holds for each prefix of a valid
// address: at most 45 bytes, at most 8 colons, no ":::", one "::", at most
// four digits between separators, at most three dots, and only decimal
// digits and dots after the first dot.
func viableIPPrefix(s string) int {
	colons, dots, group, double := 0, 0, 0, false
	for i := 0; i < len(s) && i < maxIPText; i++ {
		switch c := s[i]; c {
		case ':':
			if dots > 0 || colons == 8 {
				return i
			}
			if i > 0 && s[i-1] == ':' {
				if double || (i > 1 && s[i-2] == ':') {
					return i
				}
				double = true
			}
			colons++
			group = 0
		case '.':
			if dots == 3 {
				return i
			}
			dots++
			group = 0
		default:
			if dots > 0 && (c < '0' || c > '9') {
				return i
			}
			if group == 4 {
				return i
			}
			group++
		}
	}
	return min(len(s), maxIPText)
}

// redactStrings lists the substrings of addr worth striking from error
// text, longest first: the address as configured, its host:port without a
// scheme or trailing path, and the bare host (DNS errors mention it alone).
func redactStrings(addr string) []string {
	if addr == "" {
		return nil
	}
	out := []string{addr}
	hostport := addr
	if i := strings.Index(hostport, "://"); i >= 0 {
		hostport = hostport[i+3:]
	}
	hostport = strings.TrimRight(hostport, "/")
	if hostport != addr && hostport != "" {
		out = append(out, hostport)
	}
	if host, _, err := net.SplitHostPort(hostport); err == nil && host != "" {
		out = append(out, host)
	} else if hostport != "" {
		// No port (e.g. addr = "https://gpu.example.com"): the remainder
		// is the bare host, already covered unless a path was trimmed.
		out = append(out, hostport)
	}
	return dedupeStrings(out)
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// hostStates builds the App's HostState list from a ViewConfig, resolving
// each spec against the global toggles. With host names hidden, hosts
// display as host1, host2, … in config order; with only addresses hidden,
// just the titles that are addresses do.
func hostStates(view ViewConfig) []*HostState {
	states := make([]*HostState, 0, len(view.Hosts))
	for i, spec := range view.Hosts {
		h := NewHostState(spec.Name)
		h.view = hostView{
			gpus:          spec.GPUs,
			showGraphics:  view.ShowGraphicsProcs,
			showAddrs:     view.ShowAddresses,
			showNames:     view.ShowHostnames,
			nameFromAgent: spec.NameFromAgent,
			placeholder:   fmt.Sprintf("host%d", i+1),
		}
		if !view.ShowAddresses {
			h.view.redact = redactStrings(spec.Addr)
		}
		h.Display = h.view.title(spec.Name)
		states = append(states, h)
	}
	return states
}

// title decides what the host strip calls a host, given a candidate name:
// the configured one, or the one its agent reports. Hidden host names, and
// a candidate that is only an address while addresses are hidden, give the
// hostN placeholder instead.
func (v hostView) title(name string) string {
	if !v.showNames || v.isAddr(name) {
		return v.placeholder
	}
	return name
}

// isAddr reports whether name would show a network address that the view
// hides: an IP address as isIPForm recognises it, or, ignoring case and
// surrounding blanks, exactly one of the host's redact strings (its address
// as configured, that address's host:port without a scheme or trailing
// slash, or its bare host; a --connect host's routing name is its address).
// Other spellings of the host's own name do not match and still show, for
// example with a scheme or path around it, a trailing dot or user info.
// Always false when addresses show.
func (v hostView) isAddr(name string) bool {
	if v.showAddrs {
		return false
	}
	if isIPForm(name) {
		return true
	}
	name = strings.TrimSpace(name)
	for _, r := range v.redact {
		if strings.EqualFold(name, r) {
			return true
		}
	}
	return false
}

// isIPForm reports whether name is a dotted-quad IPv4 or a colon-separated
// IPv6 address, allowing the usual wrapping around it: blanks, a scheme, user
// info (user@, user:pw@), [brackets], a :port, an IPv6 %zone, a /path or
// /prefix, a ?query and a #fragment. It does not recognise other spellings
// of the same address: decimal, hex, octal and zero-padded IPv4 forms, a
// trailing dot, and name-shaped text such as ip-192-0-2-77 all return false,
// so they still show as titles. A reported name comes from a remote agent,
// so it is normalised before it is parsed rather than trusted to be a bare
// literal.
func isIPForm(name string) bool {
	s := strings.TrimSpace(name)
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndexByte(s, '@'); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSpace(s)
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	s = strings.TrimSpace(strings.Trim(s, "[]"))
	if i := strings.IndexByte(s, '%'); i >= 0 {
		s = s[:i]
	}
	return net.ParseIP(s) != nil
}
