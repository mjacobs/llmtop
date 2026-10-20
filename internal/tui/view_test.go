package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mjacobs/llmtop/internal/client"
	"github.com/mjacobs/llmtop/internal/config"
	"github.com/mjacobs/llmtop/internal/model"
)

// viewSnapshot builds a 2-GPU snapshot with one server per GPU and a
// graphics process on GPU 0, the shape the filter tests slice up.
func viewSnapshot(ts time.Time) *model.Snapshot {
	return &model.Snapshot{
		Host:      model.HostInfo{Name: "alpha", Hostname: "alpha"},
		Timestamp: ts,
		GPUs: []model.GPU{
			{Index: 0, Name: "demo-gpu-0", MemUsed: 8 << 30, MemTotal: 16 << 30},
			{Index: 1, Name: "demo-gpu-1", MemUsed: 4 << 30, MemTotal: 16 << 30},
		},
		Servers: []model.LLMServer{
			{Kind: "llama.cpp", Endpoint: "http://127.0.0.1:8080", Status: model.StatusOK,
				Model: "demo-model", PID: 42, Metrics: &model.LLMMetrics{PredictedTPS: 10}},
			{Kind: "llama.cpp", Endpoint: "http://127.0.0.1:8090", Status: model.StatusOK,
				Model: "demo-embed", PID: 43},
		},
		Procs: []model.GPUProc{
			{PID: 42, Name: "llama-server", GPUIndex: 0, MemBytes: 6 << 30, Class: model.ProcLLM},
			{PID: 77, Name: "Xorg", GPUIndex: 0, MemBytes: 1 << 30, Class: model.ProcGraphics},
			{PID: 43, Name: "llama-server", GPUIndex: 1, MemBytes: 3 << 30, Class: model.ProcLLM},
			{PID: 78, Name: "chrome", GPUIndex: 1, MemBytes: 1 << 29, Class: model.ProcOther},
		},
	}
}

func TestFilterSnapshotPassthrough(t *testing.T) {
	h := NewHostState("alpha")
	snap := viewSnapshot(sampleTS)
	h.Apply(client.Update{Host: "alpha", Snapshot: snap})
	if h.Snap != snap {
		t.Fatal("a default view must adopt the snapshot unmodified (same pointer)")
	}
}

func TestGPUSelectionAndOrder(t *testing.T) {
	h := NewHostState("alpha")
	h.view.gpus = []int{1, 0}
	h.Apply(client.Update{Host: "alpha", Snapshot: viewSnapshot(sampleTS)})
	if len(h.Snap.GPUs) != 2 || h.Snap.GPUs[0].Index != 1 || h.Snap.GPUs[1].Index != 0 {
		t.Fatalf("GPUs = %+v, want indices [1 0]", h.Snap.GPUs)
	}
}

func TestGPUDisableDropsPanelAndProcs(t *testing.T) {
	h := NewHostState("alpha")
	h.view.gpus = []int{1}
	h.Apply(client.Update{Host: "alpha", Snapshot: viewSnapshot(sampleTS)})

	if len(h.Snap.GPUs) != 1 || h.Snap.GPUs[0].Index != 1 {
		t.Fatalf("GPUs = %+v, want only index 1", h.Snap.GPUs)
	}
	for _, p := range h.Snap.Procs {
		if p.GPUIndex != 1 {
			t.Fatalf("proc %+v survived on hidden GPU 0", p)
		}
	}

	// The server on the hidden GPU must fall through to the host strip
	// (its PID no longer maps to any visible GPU), not vanish.
	hr := buildHostRenders([]*HostState{h})[0]
	if len(hr.byGPU[0]) != 0 {
		t.Fatalf("byGPU[0] = %+v, want empty for a hidden GPU", hr.byGPU[0])
	}
	found := false
	for _, srv := range hr.hostLevel {
		if srv.PID == 42 {
			found = true
		}
	}
	if !found {
		t.Fatal("server on the hidden GPU must become host-level, not be dropped")
	}
}

func TestHideGraphicsProcsKeepsMemTotals(t *testing.T) {
	h := NewHostState("alpha")
	h.view.showGraphics = false
	h.Apply(client.Update{Host: "alpha", Snapshot: viewSnapshot(sampleTS)})

	for _, p := range h.Snap.Procs {
		if p.Class == model.ProcGraphics {
			t.Fatalf("graphics proc %+v survived", p)
		}
	}
	if len(h.Snap.Procs) != 3 {
		t.Fatalf("procs = %d, want 3 (only the graphics row dropped)", len(h.Snap.Procs))
	}
	// The mem chart's total series reads GPU.MemUsed, which must still
	// include the hidden graphics VRAM.
	if h.Snap.GPUs[0].MemUsed != 8<<30 {
		t.Fatalf("MemUsed = %d, want the full 8GiB including graphics VRAM", h.Snap.GPUs[0].MemUsed)
	}
}

func TestHideAddressesRedactsEndpointsAndErrors(t *testing.T) {
	states := hostStates(ViewConfig{
		Hosts:             []HostSpec{{Name: "alpha", Addr: "192.0.2.40:9832"}},
		ShowAddresses:     false,
		ShowHostnames:     true,
		ShowGraphicsProcs: true,
	})
	h := states[0]
	// Hiding addresses alone keeps a configured name that is not one.
	if h.Display != "alpha" || h.Name != "alpha" {
		t.Fatalf("Display = %q Name = %q, want alpha/alpha", h.Display, h.Name)
	}

	h.Apply(client.Update{Host: "alpha", Snapshot: viewSnapshot(sampleTS)})
	if got := h.Snap.Servers[0].Endpoint; got != ":8080" {
		t.Fatalf("endpoint = %q, want :8080", got)
	}
	if _, ok := h.GenTPSHist[":8080"]; !ok {
		t.Fatal("gen-t/s history must be keyed by the redacted endpoint")
	}

	err := errors.New(`Get "http://192.0.2.40:9832/v1/snapshot": dial tcp 192.0.2.40:9832: connect: connection refused`)
	h.Apply(client.Update{Host: "alpha", Err: err})
	if want := `Get "http://…/v1/snapshot": dial tcp …: connect: connection refused`; h.ErrMsg != want {
		t.Fatalf("ErrMsg = %q, want %q", h.ErrMsg, want)
	}
}

func TestShowAddressesLeavesDisplayAlone(t *testing.T) {
	states := hostStates(ViewConfig{
		Hosts:             []HostSpec{{Name: "alpha", Addr: "192.0.2.40:9832"}},
		ShowAddresses:     true,
		ShowHostnames:     true,
		ShowGraphicsProcs: true,
	})
	h := states[0]
	if h.Display != "alpha" {
		t.Fatalf("Display = %q, want alpha", h.Display)
	}
	h.Apply(client.Update{Host: "alpha", Snapshot: viewSnapshot(sampleTS)})
	if got := h.Snap.Servers[0].Endpoint; got != "http://127.0.0.1:8080" {
		t.Fatalf("endpoint = %q, want unredacted", got)
	}
}

// namedSnapshot is viewSnapshot as reported by an agent that calls itself
// name, at timestamp ts.
func namedSnapshot(name string, ts time.Time) *model.Snapshot {
	s := viewSnapshot(ts)
	s.Host = model.HostInfo{Name: name, Hostname: name}
	return s
}

func TestHideHostnamesUsesPlaceholders(t *testing.T) {
	states := hostStates(ViewConfig{
		Hosts: []HostSpec{
			{Name: "alpha"},
			{Name: "192.0.2.50:9832", Addr: "192.0.2.50:9832", NameFromAgent: true},
		},
		ShowAddresses:     true,
		ShowHostnames:     false,
		ShowGraphicsProcs: true,
	})
	if states[0].Display != "host1" || states[1].Display != "host2" {
		t.Fatalf("titles = %q, %q, want host1, host2", states[0].Display, states[1].Display)
	}
	// The agent's own name is a host name too: it must not leak in.
	states[1].Apply(client.Update{Host: "192.0.2.50:9832", Snapshot: namedSnapshot("bravo", sampleTS)})
	if states[1].Display != "host2" {
		t.Fatalf("title after snapshot = %q, want host2", states[1].Display)
	}
	// Endpoints stay unredacted: the two toggles are independent.
	if got := states[1].Snap.Servers[0].Endpoint; got != "http://127.0.0.1:8080" {
		t.Fatalf("endpoint = %q, want unredacted", got)
	}
}

func TestConnectHostTakesAgentName(t *testing.T) {
	const addr = "192.0.2.50:9832"
	states := hostStates(ViewConfig{
		Hosts:             []HostSpec{{Name: addr, Addr: addr, NameFromAgent: true}},
		ShowAddresses:     true,
		ShowHostnames:     true,
		ShowGraphicsProcs: true,
	})
	h := states[0]
	if h.Display != addr {
		t.Fatalf("title before any snapshot = %q, want the address %q", h.Display, addr)
	}

	h.Apply(client.Update{Host: addr, Snapshot: namedSnapshot("bravo", sampleTS)})
	if h.Display != "bravo" || h.Name != addr {
		t.Fatalf("Display = %q Name = %q, want bravo/%s (routing key unchanged)", h.Display, h.Name, addr)
	}

	// A failed poll keeps the last known name.
	h.Apply(client.Update{Host: addr, Err: errors.New("connection refused")})
	if h.Display != "bravo" {
		t.Fatalf("title after an error = %q, want bravo", h.Display)
	}

	// An agent that sends no Name falls back to its Hostname; one that
	// sends neither leaves the title alone.
	s := viewSnapshot(sampleTS.Add(time.Second))
	s.Host = model.HostInfo{Hostname: "charlie"}
	h.Apply(client.Update{Host: addr, Snapshot: s})
	if h.Display != "charlie" {
		t.Fatalf("title from Hostname = %q, want charlie", h.Display)
	}
	s = viewSnapshot(sampleTS.Add(2 * time.Second))
	s.Host = model.HostInfo{}
	h.Apply(client.Update{Host: addr, Snapshot: s})
	if h.Display != "charlie" {
		t.Fatalf("title after a nameless snapshot = %q, want charlie", h.Display)
	}
}

func TestConfiguredHostKeepsItsName(t *testing.T) {
	states := hostStates(ViewConfig{
		Hosts:             []HostSpec{{Name: "alpha", Addr: "192.0.2.40:9832"}},
		ShowAddresses:     true,
		ShowHostnames:     true,
		ShowGraphicsProcs: true,
	})
	h := states[0]
	h.Apply(client.Update{Host: "alpha", Snapshot: namedSnapshot("bravo", sampleTS)})
	if h.Display != "alpha" {
		t.Fatalf("title = %q, want the configured name alpha", h.Display)
	}
}

// With addresses hidden but names shown, any title that is only an address
// falls back to its placeholder; real names still show.
func TestHideAddressesHidesAddressTitles(t *testing.T) {
	states := hostStates(ViewConfig{
		Hosts: []HostSpec{
			{Name: "192.0.2.40", Addr: "gpu.example.com:9832"},
			{Name: "192.0.2.50:9832", Addr: "192.0.2.50:9832", NameFromAgent: true},
			{Name: "gpu.example.com:9832", Addr: "gpu.example.com:9832", NameFromAgent: true},
			// A configured name that is the host part of its own address
			// would show that address, so it hides too.
			{Name: "gpu1", Addr: "gpu1:9832"},
		},
		ShowAddresses:     false,
		ShowHostnames:     true,
		ShowGraphicsProcs: true,
	})
	for i, want := range []string{"host1", "host2", "host3", "host4"} {
		if states[i].Display != want {
			t.Fatalf("title %d = %q, want %q", i, states[i].Display, want)
		}
	}

	// The --connect host shows its agent's name once it reports one...
	states[1].Apply(client.Update{Host: "192.0.2.50:9832", Snapshot: namedSnapshot("bravo", sampleTS)})
	if states[1].Display != "bravo" {
		t.Fatalf("title = %q, want bravo", states[1].Display)
	}
	// ...unless that name is itself an address: an IP literal, or the
	// address it was reached at, in any letter case.
	states[1].Apply(client.Update{Host: "192.0.2.50:9832", Snapshot: namedSnapshot("192.0.2.50", sampleTS.Add(time.Second))})
	if states[1].Display != "host2" {
		t.Fatalf("title for an IP-named agent = %q, want host2", states[1].Display)
	}
	states[2].Apply(client.Update{Host: "gpu.example.com:9832", Snapshot: namedSnapshot("GPU.example.com", sampleTS)})
	if states[2].Display != "host3" {
		t.Fatalf("title for an agent named after its address = %q, want host3", states[2].Display)
	}
}

// With addresses hidden, an agent-reported name that is an address in any
// form never becomes the title; the host falls back to its placeholder.
// Real names, and the same text with addresses shown, still pass through.
func TestHiddenAddressesNeverTitleAnAddress(t *testing.T) {
	const own = "192.0.2.50:9832"
	cases := []struct {
		reported string
		connect  string // the --connect target; own when empty
		hidden   bool   // want the hostN placeholder with addresses hidden
	}{
		// Address forms.
		{reported: "192.0.2.77", hidden: true},
		{reported: "192.0.2.77:9832", hidden: true},
		{reported: "192.0.2.77:", hidden: true},
		{reported: " 192.0.2.77", hidden: true},
		{reported: "192.0.2.77\t ", hidden: true},
		{reported: "192.0.2.77/24", hidden: true},
		{reported: "http://192.0.2.77:9832", hidden: true},
		{reported: "2001:db8::9", hidden: true},
		{reported: "2001:DB8::9", hidden: true},
		{reported: "[2001:db8::9]", hidden: true},
		{reported: "[2001:db8::9]:9832", hidden: true},
		{reported: "2001:db8::/32", hidden: true},
		{reported: "fe80::1%eth0", hidden: true},
		{reported: "[fe80::1%eth0]:9832", hidden: true},
		{reported: "::ffff:192.0.2.77", hidden: true},
		{reported: "user@192.0.2.77", hidden: true},
		{reported: "user:pw@192.0.2.77:9832", hidden: true},
		{reported: "http://user:pw@192.0.2.77:9832/", hidden: true},
		{reported: "a@b@192.0.2.77", hidden: true},
		{reported: "user@[2001:db8::9]:9832", hidden: true},
		{reported: "192.0.2.77?x=1", hidden: true},
		{reported: "192.0.2.77#f", hidden: true},
		{reported: "192.0.2.77?u=a@b", hidden: true},
		// The host's own address, in any letter case.
		{reported: own, hidden: true},
		{reported: "192.0.2.50", hidden: true},
		{reported: "alpha:9832", connect: "alpha:9832", hidden: true},
		{reported: "alpha", connect: "alpha:9832", hidden: true},
		{reported: "ALPHA", connect: "alpha:9832", hidden: true},
		{reported: "GPU.example.com", connect: "https://gpu.example.com:9832/", hidden: true},
		// No name: the title never falls back to the address.
		{reported: "", hidden: true},
		{reported: "  ", hidden: true},
		// Names that are not addresses keep showing.
		{reported: "bravo"},
		{reported: "gpu-01"},
		{reported: "gpu.example.com"},
		{reported: "ip-192-0-2-77"},
		{reported: "node:1"},
		{reported: "1.2.3"},
		{reported: "fe80"},
		{reported: "user@bravo"},
		{reported: "bravo?x=1"},
		{reported: "bravo#f"},
		{reported: "a@b"},
		// Spellings the check does not recognise still show (see isIPForm).
		{reported: "3221225805"},
		{reported: "0xC000024D"},
		{reported: "192.000.002.077"},
		{reported: "192.0.2.77."},
		{reported: "alpha", connect: "bravo:9832"},
	}
	for _, c := range cases {
		connect := c.connect
		if connect == "" {
			connect = own
		}
		t.Run(fmt.Sprintf("%q", c.reported), func(t *testing.T) {
			for _, showAddrs := range []bool{false, true} {
				states := hostStates(ViewConfig{
					Hosts:             []HostSpec{{Name: connect, Addr: connect, NameFromAgent: true}},
					ShowAddresses:     showAddrs,
					ShowHostnames:     true,
					ShowGraphicsProcs: true,
				})
				h := states[0]
				h.Apply(client.Update{Host: connect, Snapshot: namedSnapshot(c.reported, sampleTS)})
				want := strings.TrimSpace(c.reported)
				switch {
				case !showAddrs && c.hidden:
					want = "host1"
				case want == "":
					want = connect // nothing reported: the title is unchanged
				}
				if h.Display != want {
					t.Fatalf("show_addresses=%v: title = %q, want %q", showAddrs, h.Display, want)
				}
			}
		})
	}
}

// TestScrubErrNamedHostDialFailure dials a host given by name through the
// real remote client, so the error is Go's own text: for a name, the dial
// error carries the address the name resolved to, which the configured
// address strings never mention. "localhost" is the one name that
// resolves without DNS; the port is one just closed, so the dial fails.
func TestScrubErrNamedHostDialFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close()
	addr := "localhost:" + port

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan client.Update, 1)
	client.StartRemote(ctx, "alpha", addr, "", time.Hour, ch)
	var u client.Update
	select {
	case u = <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("no update from the remote client")
	}
	cancel()
	if u.Err == nil {
		t.Fatalf("dial to %s succeeded, want a failure", addr)
	}
	raw := u.Err.Error()
	// Go reports the resolved address it dialed, not the name.
	if !strings.Contains(raw, "dial tcp 127.0.0.1:"+port) && !strings.Contains(raw, "dial tcp [::1]:"+port) {
		t.Fatalf("Go's dial error %q carries no resolved address; the test needs one", raw)
	}

	h := hostStates(ViewConfig{
		Hosts:             []HostSpec{{Name: "alpha", Addr: addr}},
		ShowAddresses:     false,
		ShowHostnames:     true,
		ShowGraphicsProcs: true,
	})[0]
	h.Apply(u)
	for _, leak := range []string{"127.0.0.1", "::1", "localhost"} {
		if strings.Contains(h.ErrMsg, leak) {
			t.Errorf("ErrMsg = %q shows %q (from %q)", h.ErrMsg, leak, raw)
		}
	}
	if !strings.Contains(h.ErrMsg, "dial tcp …") || !strings.Contains(h.ErrMsg, "connection refused") {
		t.Errorf("ErrMsg = %q, want the error kept apart from its addresses (from %q)", h.ErrMsg, raw)
	}
}

// TestScrubErrForms pins scrubErr on every form of address an error can
// carry while addresses are hidden. The host's configured address is a
// name, so each IP here is scrubbed by the generic rule, as a resolved
// address would be.
func TestScrubErrForms(t *testing.T) {
	v := hostStates(ViewConfig{
		Hosts:             []HostSpec{{Name: "alpha", Addr: "alpha.example.com:9832"}},
		ShowAddresses:     false,
		ShowHostnames:     true,
		ShowGraphicsProcs: true,
	})[0].view
	cases := []struct{ name, in, want string }{
		{"ipv4 with port", "dial tcp 192.0.2.10:9832: connect: connection refused",
			"dial tcp …: connect: connection refused"},
		{"ipv4 without port", "no route to host 192.0.2.10",
			"no route to host …"},
		{"bracketed ipv6 with port", "dial tcp [2001:db8::10]:9832: connect: connection refused",
			"dial tcp …: connect: connection refused"},
		{"bracketed ipv6 without port", "peer [2001:db8::10] went away",
			"peer … went away"},
		{"bare ipv6", "no route to 2001:db8::10 now", "no route to … now"},
		{"ipv6 ending in ::", "network 2001:db8:: is down", "network … is down"},
		{"ipv6 zone with port", "dial tcp [fe80::1%eth0]:9832: connect: network is unreachable",
			"dial tcp …: connect: network is unreachable"},
		{"bare ipv6 zone", "neighbour fe80::1%br-lan lost", "neighbour … lost"},
		{"ipv4-mapped ipv6", "dial tcp [::ffff:192.0.2.10]:9832: i/o timeout",
			"dial tcp …: i/o timeout"},
		{"bare ipv4-mapped ipv6", "from ::ffff:192.0.2.10 only", "from … only"},
		{"loopback ipv6", "dial tcp [::1]:9832: connect: connection refused",
			"dial tcp …: connect: connection refused"},
		{"ipv4 in url", `Get "http://192.0.2.10:9832/api/v1/snapshot": EOF`,
			`Get "http://…/api/v1/snapshot": EOF`},
		{"ipv6 in url", `Get "http://[2001:db8::10]:9832/api/v1/snapshot": EOF`,
			`Get "http://…/api/v1/snapshot": EOF`},
		{"zone in url", `Get "http://[fe80::1%25eth0]:9832/": EOF`, `Get "http://…/": EOF`},
		{"user info in url", "http://user@192.0.2.10/x: HTTP 503", "http://user@…/x: HTTP 503"},
		{"lookup on resolver", "lookup alpha.example.com on 192.0.2.53:53: no such host",
			"lookup … on …: no such host"},
		{"lookup on ipv6 resolver", "lookup alpha.example.com on [::1]:53: server misbehaving",
			"lookup … on …: server misbehaving"},
		{"after ->", "read tcp 192.0.2.1:50312->192.0.2.10:9832: read: connection reset by peer",
			"read tcp …->…: read: connection reset by peer"},
		{"after -> ipv6", "write tcp [2001:db8::1]:50312->[2001:db8::10]:9832: broken pipe",
			"write tcp …->…: broken pipe"},
		{"several", `Get "http://alpha.example.com:9832/x": dial tcp 192.0.2.10:9832: connect: no route; also [2001:db8::10]:9832`,
			`Get "http://…/x": dial tcp …: connect: no route; also …`},
		{"parentheses", "refused (192.0.2.10)", "refused (…)"},
		{"quotes and comma", `"192.0.2.10", '2001:db8::10'`, `"…", '…'`},
		{"sentence end", "cannot reach 192.0.2.10.", "cannot reach …."},
		{"ipv6 at sentence end", "cannot reach 2001:db8::10.", "cannot reach …."},
		{"angle brackets", "<192.0.2.10>", "<…>"},
		{"key=value", "addr=192.0.2.10; port=9832", "addr=…; port=9832"},
		{"glued by colon", "host:192.0.2.10 failed", "host:… failed"},
		{"ipv6 glued by colon", "addr:2001:db8::10 failed", "addr:… failed"},
		{"range", "192.0.2.1-192.0.2.9", "…-…"},
		{"upper-case ipv6", "dial tcp [2001:DB8::A]:9832: refused", "dial tcp …: refused"},
		// Text that only looks like an address stays.
		{"no address", "context deadline exceeded (Client.Timeout exceeded while awaiting headers)",
			"context deadline exceeded (Client.Timeout exceeded while awaiting headers)"},
		{"clock and version", "at 12:30:45 go1.27.2 HTTP 503", "at 12:30:45 go1.27.2 HTTP 503"},
		{"other host name", "lookup bravo.example.com: no such host", "lookup bravo.example.com: no such host"},
	}
	for _, c := range cases {
		if got := v.scrubErr(c.in); got != c.want {
			t.Errorf("%s: scrubErr(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}

	shown := hostStates(ViewConfig{
		Hosts:             []HostSpec{{Name: "alpha", Addr: "192.0.2.40:9832"}},
		ShowAddresses:     true,
		ShowHostnames:     true,
		ShowGraphicsProcs: true,
	})[0].view
	const msg = "dial tcp 192.0.2.40:9832: connect: connection refused"
	if got := shown.scrubErr(msg); got != msg {
		t.Errorf("with addresses shown, scrubErr(%q) = %q, want it unchanged", msg, got)
	}
}

// TestScrubErrLongInput bounds the scrub's cost on every input shape. The
// scan tests up to maxAddrPiece pieces at each piece start, so its cost
// depends on the shape: a run of "::" starts a piece at every byte and was
// about eleven times slower than dotted numbers (2.5 s for 500 KB). The
// shapes here include that worst one; each must finish quickly and come
// back bounded by the cap (maxScrubLen), with an address before the cut
// scrubbed and one past it dropped.
func TestScrubErrLongInput(t *testing.T) {
	v := hostStates(ViewConfig{
		Hosts:             []HostSpec{{Name: "alpha", Addr: "alpha.example.com:9832"}},
		ShowAddresses:     false,
		ShowHostnames:     true,
		ShowGraphicsProcs: true,
	})[0].view
	const size = 512 << 10
	for _, shape := range []string{"::", ":", "1-", "1_", "1.2:", "9999."} {
		// Runs of about 1 KB, so the scan works through the whole cap
		// rather than dropping one run that crosses it.
		chunk := strings.Repeat(shape, 1000/len(shape)) + " "
		long := "dial tcp 192.0.2.10:9832: " + strings.Repeat(chunk, size/len(chunk)) + " 192.0.2.11"
		start := time.Now()
		got := v.scrubErr(long)
		d := time.Since(start)
		if strings.Contains(got, "192.0.2.1") {
			t.Errorf("shape %q: an address survived in %.80q…", shape, got)
		}
		if !strings.HasPrefix(got, "dial tcp …: ") {
			t.Errorf("shape %q: scrubErr = %.80q…, want it to start with the scrubbed dial error", shape, got)
		}
		// Every byte out comes from the first maxScrubLen bytes in; a
		// two-byte "::" becoming a three-byte "…" can grow it by half.
		if limit := 2 * maxScrubLen; len(got) > limit {
			t.Errorf("shape %q: %d bytes out, want at most %d", shape, len(got), limit)
		}
		if d > time.Second {
			t.Errorf("shape %q: scrubErr took %v on %d bytes", shape, d, len(long))
		}
	}
}

// TestScrubIPsCapCut pins where capScrubInput cuts: an address that
// straddles the cap is dropped whole rather than shown in part, and a
// multi-byte character at the cap is not split.
func TestScrubIPsCapCut(t *testing.T) {
	pad := strings.Repeat("x ", maxScrubLen/2)[:maxScrubLen-4]
	got := scrubIPs(pad + " 192.0.2.10 tail")
	if strings.Contains(got, "192") || strings.Contains(got, "tail") {
		t.Errorf("address across the cap leaked: ...%q", got[len(got)-20:])
	}
	if !strings.HasSuffix(got, " …") {
		t.Errorf("cut text ends %q, want the run dropped and a … marker", got[len(got)-20:])
	}

	wide := strings.Repeat("x", maxScrubLen-1) + "é" + strings.Repeat("y", 10)
	got = scrubIPs(wide)
	if !utf8.ValidString(got) {
		t.Errorf("cut split a UTF-8 sequence: ...%q", got[len(got)-8:])
	}
	if len(got) > maxScrubLen+len("…") {
		t.Errorf("%d bytes out, want at most %d", len(got), maxScrubLen+len("…"))
	}

	const short = "dial tcp 192.0.2.10:9832: refused"
	if got := scrubIPs(short); got != "dial tcp …: refused" {
		t.Errorf("scrubIPs(%q) = %q; text under the cap must not be cut", short, got)
	}
}

// TestHideAddressesScrubsCollectionErrors covers the "! …" lines a host
// shows for its own collection errors (Snapshot.Errors). With addresses
// hidden they go through scrubErr like the connection-error banner, so a
// collector or agent error that names an address does not show it; with
// addresses shown they pass through unchanged. The delivered snapshot is
// never modified.
func TestHideAddressesScrubsCollectionErrors(t *testing.T) {
	errs := []string{
		"proc scan: dial tcp 192.0.2.10:9832: connect: connection refused",
		"gpu sample: lookup alpha.example.com on [2001:db8::53]:53: no such host",
		"nvml: driver not loaded",
	}
	snap := func() *model.Snapshot {
		s := viewSnapshot(sampleTS)
		s.Errors = append([]string(nil), errs...)
		return s
	}
	cfg := func(show bool) *HostState {
		return hostStates(ViewConfig{
			Hosts:             []HostSpec{{Name: "alpha", Addr: "alpha.example.com:9832"}},
			ShowAddresses:     show,
			ShowHostnames:     true,
			ShowGraphicsProcs: true,
		})[0]
	}

	h := cfg(false)
	in := snap()
	h.Apply(client.Update{Host: "alpha", Snapshot: in})
	want := []string{
		"proc scan: dial tcp …: connect: connection refused",
		"gpu sample: lookup … on …: no such host",
		"nvml: driver not loaded",
	}
	if !reflect.DeepEqual(h.Snap.Errors, want) {
		t.Errorf("Errors = %q, want %q", h.Snap.Errors, want)
	}
	if !reflect.DeepEqual(in.Errors, errs) {
		t.Errorf("the delivered snapshot's Errors changed to %q", in.Errors)
	}
	out := RenderOverview([]*HostState{h}, 0, 200, 60, LayoutPref{}, DefaultTheme())
	for _, leak := range []string{"192.0.2.10", "2001:db8", "alpha.example.com"} {
		if strings.Contains(out, leak) {
			t.Errorf("overview shows %q in a collection-error line:\n%s", leak, out)
		}
	}
	if !strings.Contains(out, "! proc scan: dial tcp …") {
		t.Errorf("overview lacks the scrubbed collection-error line:\n%s", out)
	}

	shown := cfg(true)
	shown.Apply(client.Update{Host: "alpha", Snapshot: snap()})
	if !reflect.DeepEqual(shown.Snap.Errors, errs) {
		t.Errorf("with addresses shown, Errors = %q, want %q unchanged", shown.Snap.Errors, errs)
	}
}

func TestRedactStrings(t *testing.T) {
	cases := []struct {
		addr string
		want []string
	}{
		{"", nil},
		{"192.0.2.40:9832", []string{"192.0.2.40:9832", "192.0.2.40"}},
		{"http://192.0.2.40:9832", []string{"http://192.0.2.40:9832", "192.0.2.40:9832", "192.0.2.40"}},
		{"https://gpu.example.com", []string{"https://gpu.example.com", "gpu.example.com"}},
	}
	for _, c := range cases {
		if got := redactStrings(c.addr); !reflect.DeepEqual(got, c.want) {
			t.Errorf("redactStrings(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestViewFor(t *testing.T) {
	cfg := config.Config{
		Hosts: []config.HostConfig{
			{Name: "alpha", Local: true, GPUs: []int{1}},
			{Name: "bravo", Addr: "192.0.2.40:9832"},
		},
		UI: config.UIConfig{ShowAddresses: false, ShowHostnames: true, ShowGraphicsProcs: true},
	}

	v := viewFor(cfg, []string{"192.0.2.50:9832"}, false)
	want := []HostSpec{
		{Name: "alpha", GPUs: []int{1}},
		{Name: "bravo", Addr: "192.0.2.40:9832"},
		{Name: "192.0.2.50:9832", Addr: "192.0.2.50:9832", NameFromAgent: true},
	}
	if !reflect.DeepEqual(v.Hosts, want) {
		t.Fatalf("hosts = %+v, want %+v", v.Hosts, want)
	}
	if v.ShowAddresses || !v.ShowHostnames || !v.ShowGraphicsProcs {
		t.Fatalf("toggles = %+v, want carried through from cfg.UI", v)
	}

	// Demo hosts are fixed and carry no per-host gpus list, but the global
	// toggles still apply.
	d := viewFor(cfg, nil, true)
	if len(d.Hosts) != 2 || d.Hosts[0].Name != "demo-rig" || d.Hosts[1].Name != "demo-node" {
		t.Fatalf("demo hosts = %+v", d.Hosts)
	}
	if d.ShowAddresses {
		t.Fatal("demo mode must still honour show_addresses=false")
	}
}

func TestLayoutPrefFor(t *testing.T) {
	for name, tc := range map[string]struct {
		in   config.LayoutConfig
		want LayoutPref
	}{
		"empty is auto":    {want: LayoutPref{Mode: LayoutAuto}},
		"one-row":          {in: config.LayoutConfig{Mode: config.LayoutModeOneRow}, want: LayoutPref{Mode: LayoutOneRow}},
		"stack-by-host":    {in: config.LayoutConfig{Mode: config.LayoutModeStackByHost}, want: LayoutPref{Mode: LayoutStackByHost}},
		"columns explicit": {in: config.LayoutConfig{Mode: config.LayoutModeColumns, Columns: 3}, want: LayoutPref{Mode: LayoutColumns, Columns: 3}},
		"columns implied":  {in: config.LayoutConfig{Columns: 2}, want: LayoutPref{Mode: LayoutColumns, Columns: 2}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := layoutPrefFor(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("layoutPrefFor(%+v) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}

	got := layoutPrefFor(config.LayoutConfig{
		Mode:  config.LayoutModeOneRow,
		Slots: []config.LayoutSlot{{Host: "alpha", GPU: 1}, {Host: "bravo", GPU: 0}},
	})
	want := LayoutPref{Mode: LayoutOneRow, Slots: []LayoutSlot{{Host: "alpha", GPU: 1}, {Host: "bravo", GPU: 0}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("slots = %+v, want %+v", got, want)
	}
}

// TestViewForCarriesLayout pins that the [layout] section reaches the App.
func TestViewForCarriesLayout(t *testing.T) {
	cfg := config.Config{Layout: config.LayoutConfig{Mode: config.LayoutModeStackByHost}}
	if got := viewFor(cfg, nil, false).Layout.Mode; got != LayoutStackByHost {
		t.Fatalf("view layout mode = %v, want stack-by-host", got)
	}
	if got := viewFor(cfg, nil, true).Layout.Mode; got != LayoutStackByHost {
		t.Fatalf("demo mode must honour [layout] too, got %v", got)
	}
}

// exhaustiveAddrPiece is longestAddrPiece as it was before addrPieceEnds:
// every end tested with isIPForm. TestAddrPieceEndsMatchExhaustive holds
// the fast version to it.
func exhaustiveAddrPiece(run string, start int) int {
	for end := min(len(run), start+maxAddrPiece); end > start; end-- {
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

// TestAddrPieceEndsMatchExhaustive compares longestAddrPiece with testing
// every end, at every piece start of random runs built from address
// fragments and single address characters.
func TestAddrPieceEndsMatchExhaustive(t *testing.T) {
	frags := []string{
		"192.0.2.10", "10.0.0.1", "255.255.255.255", "1.2.3", "01.2.3.4", "2001:db8::10",
		"::", "::1", "fe80::1", "::ffff:192.0.2.10", "0:0:0:0:0:ffff:1.2.3.4",
		"1:2:3:4:5:6:7:8", "1:2:3:4:5:6:7::", "ffff:ffff:ffff:ffff:ffff:ffff:255.255.255.255",
		"%eth0", "%25lo", "%", "[", "]", "]:", ":", ":9832", ":80", ".", "-", "_",
		"a", "f", "g", "x", "0", "9", "12345", "deadbeef", "eth0", "br-lan",
	}
	const alphabet = "0123456789abcdefABCDEFgxz.:%[]-_"
	rng := rand.New(rand.NewPCG(1, 2))
	for n := 0; n < 100000; n++ {
		var b strings.Builder
		for parts := 1 + rng.IntN(8); parts > 0; parts-- {
			if rng.IntN(3) == 0 {
				b.WriteByte(alphabet[rng.IntN(len(alphabet))])
			} else {
				b.WriteString(frags[rng.IntN(len(frags))])
			}
		}
		run := b.String()
		for start := 0; start < len(run); start++ {
			if start > 0 && !isPieceEdge(run[start-1]) {
				continue
			}
			if got, want := longestAddrPiece(run, start), exhaustiveAddrPiece(run, start); got != want {
				t.Fatalf("run %q start %d: longestAddrPiece = %d, testing every end gives %d", run, start, got, want)
			}
		}
	}
}

// TestHiddenAddressesBoundSnapshotWork feeds Apply the snapshot a hostile
// agent can send: 2,000 collection errors of 4,000 bytes each in the
// slowest shape, every one carrying an address, and servers whose status,
// role and metrics error carry addresses too. Hiding addresses must stay
// cheap per snapshot (it took 36 s before the per-snapshot budget), show
// at most maxErrorLines errors plus a count of the rest, and show no
// address anywhere.
func TestHiddenAddressesBoundSnapshotWork(t *testing.T) {
	h := hostStates(ViewConfig{
		Hosts:             []HostSpec{{Name: "alpha", Addr: "alpha.example.com:9832"}},
		ShowAddresses:     false,
		ShowHostnames:     true,
		ShowGraphicsProcs: true,
	})[0]
	chunk := strings.Repeat("::", 499) + " "
	body := strings.Repeat(chunk, 4)[:3960]
	s := viewSnapshot(sampleTS)
	for i := 0; i < 2000; i++ {
		s.Errors = append(s.Errors, fmt.Sprintf("agent error %d: dial tcp 192.0.2.10:9832: %s", i, body))
	}
	for i := 0; i < 300; i++ {
		s.Servers = append(s.Servers, model.LLMServer{
			Kind: "llama.cpp", Endpoint: "http://192.0.2.10:8080",
			Status:     "down since dial tcp 192.0.2.11:8080 failed " + body,
			Role:       "proxy for [2001:db8::12]:80",
			MetricsErr: `Get "http://alpha.example.com:8080/metrics": dial tcp 192.0.2.13:8080: refused`,
		})
	}

	start := time.Now()
	h.Apply(client.Update{Host: "alpha", Snapshot: s})
	if d := time.Since(start); d > time.Second {
		t.Errorf("Apply took %v", d)
	}

	if n := len(h.Snap.Errors); n != maxErrorLines+1 {
		t.Fatalf("%d error lines kept, want %d plus a count", n, maxErrorLines)
	}
	if last, want := h.Snap.Errors[maxErrorLines], fmt.Sprintf("… %d more errors", 2000-maxErrorLines); last != want {
		t.Errorf("last error line = %q, want %q", last, want)
	}
	shown := strings.Join(h.Snap.Errors, "\n")
	for _, srv := range h.Snap.Servers {
		shown += "\n" + srv.Endpoint + " " + srv.Status + " " + srv.Role + " " + srv.MetricsErr
	}
	for _, leak := range []string{"192.0.2.", "2001:db8", "alpha.example.com"} {
		if strings.Contains(shown, leak) {
			t.Errorf("hidden address %q shown", leak)
		}
	}
	if !strings.Contains(h.Snap.Errors[0], "agent error 0: dial tcp …: ") {
		t.Errorf("first error line = %.60q…, want it kept and scrubbed", h.Snap.Errors[0])
	}
}

// TestHideAddressesScrubsServerStrings covers the other error and status
// text the panels draw: a server's metrics error, a status the collector
// does not set and its role. With addresses hidden they lose their
// addresses; the collector's own states pass as they are; with addresses
// shown nothing changes.
func TestHideAddressesScrubsServerStrings(t *testing.T) {
	snap := func() *model.Snapshot {
		s := viewSnapshot(sampleTS)
		s.Servers[0].MetricsErr = `Get "http://192.0.2.10:8080/metrics": EOF`
		s.Servers[0].Status = "stuck on [2001:db8::10]:8080"
		s.Servers[0].Role = "router"
		s.Servers[1].Status = model.StatusSleeping
		s.Servers[1].Role = "worker of 192.0.2.10"
		return s
	}
	cfg := func(show bool) *HostState {
		return hostStates(ViewConfig{
			Hosts:             []HostSpec{{Name: "alpha", Addr: "alpha.example.com:9832"}},
			ShowAddresses:     show,
			ShowHostnames:     true,
			ShowGraphicsProcs: true,
		})[0]
	}

	h := cfg(false)
	h.Apply(client.Update{Host: "alpha", Snapshot: snap()})
	s0, s1 := h.Snap.Servers[0], h.Snap.Servers[1]
	if want := `Get "http://…/metrics": EOF`; s0.MetricsErr != want {
		t.Errorf("MetricsErr = %q, want %q", s0.MetricsErr, want)
	}
	if want := "stuck on …"; s0.Status != want {
		t.Errorf("Status = %q, want %q", s0.Status, want)
	}
	if s0.Role != "router" || s1.Status != model.StatusSleeping {
		t.Errorf("known role/status changed: %q, %q", s0.Role, s1.Status)
	}
	if want := "worker of …"; s1.Role != want {
		t.Errorf("Role = %q, want %q", s1.Role, want)
	}
	out := RenderOverview([]*HostState{h}, 0, 200, 60, LayoutPref{}, DefaultTheme())
	for _, leak := range []string{"192.0.2.10", "2001:db8"} {
		if strings.Contains(out, leak) {
			t.Errorf("overview shows %q:\n%s", leak, out)
		}
	}

	shown := cfg(true)
	shown.Apply(client.Update{Host: "alpha", Snapshot: snap()})
	if !reflect.DeepEqual(shown.Snap.Servers, snap().Servers) {
		t.Errorf("with addresses shown, servers changed: %+v", shown.Snap.Servers)
	}
}

// Each field must actually reach the rendered screen in the shown-address
// control; otherwise truncation or a hidden process table could mask a leak.
func TestHideAddressesScrubsDisplayFields(t *testing.T) {
	fields := []struct {
		name string
		set  func(*model.Snapshot, string)
	}{
		{"model", func(s *model.Snapshot, v string) { s.Servers[0].Model = v }},
		{"kind", func(s *model.Snapshot, v string) { s.Servers[0].Kind = v }},
		{"GPU name", func(s *model.Snapshot, v string) { s.GPUs[0].Name = v }},
		{"process name", func(s *model.Snapshot, v string) { s.Procs[0].Name = v }},
		{"driver version", func(s *model.Snapshot, v string) { s.Host.DriverVersion = v }},
	}
	for _, field := range fields {
		t.Run(field.name, func(t *testing.T) {
			for _, addr := range []string{"192.0.2.10", "2001:db8::10", "alpha.example.com"} {
				in := viewSnapshot(sampleTS)
				field.set(in, addr)
				before, err := json.Marshal(in)
				if err != nil {
					t.Fatal(err)
				}
				for _, show := range []bool{false, true} {
					h := hostStates(ViewConfig{
						Hosts:             []HostSpec{{Name: "alpha", Addr: "alpha.example.com:9832"}},
						ShowAddresses:     show,
						ShowHostnames:     true,
						ShowGraphicsProcs: true,
					})[0]
					h.Apply(client.Update{Host: "alpha", Snapshot: in})
					out := RenderOverview([]*HostState{h}, 0, 240, 80, LayoutPref{}, DefaultTheme())
					if strings.Contains(out, addr) != show {
						t.Errorf("show_addresses=%v, address %q presence differs:\n%s", show, addr, out)
					}
					if show && h.Snap != in {
						t.Error("shown-address view must retain the original snapshot")
					}
					after, err := json.Marshal(in)
					if err != nil {
						t.Fatal(err)
					}
					if string(after) != string(before) {
						t.Fatal("filter mutated the caller's snapshot")
					}
				}
			}
		})
	}
}

// Recognized engine kinds select metrics advice as well as labeling the
// server. Like known statuses and the router role, they must retain their
// meaning even when they match a host name or the text budget is spent.
func TestHideAddressesPreservesEngineHints(t *testing.T) {
	for _, kind := range []string{"llama.cpp", "vllm", "ninfer"} {
		for _, exhausted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/exhausted=%v", kind, exhausted), func(t *testing.T) {
				s := viewSnapshot(sampleTS)
				s.GPUs = s.GPUs[:1]
				s.Procs = s.Procs[:1]
				s.Servers = []model.LLMServer{{
					Kind: kind, Endpoint: "http://192.0.2.10:8080", PID: 42,
					Status: model.StatusOK, CtxSize: 8192, SlotsTotal: 4,
				}}
				addr := kind + ":9832" // Reproduce a service name colliding with its engine.
				if exhausted {
					addr = "alpha.example.com:9832"
					for i := 0; i < scrubBudget/maxScrubLen; i++ {
						s.Errors = append(s.Errors, strings.Repeat("x ", maxScrubLen/2))
					}
				}
				h := hostStates(ViewConfig{
					Hosts:             []HostSpec{{Name: "alpha", Addr: addr}},
					ShowHostnames:     true,
					ShowGraphicsProcs: true,
				})[0]
				h.Apply(client.Update{Host: "alpha", Snapshot: s})
				got := &h.Snap.Servers[0]
				if got.Kind != kind {
					t.Errorf("Kind = %q, want %q", got.Kind, kind)
				}
				th := DefaultTheme()
				original := &s.Servers[0]
				if got, want := serverLine2(got, th), serverLine2(original, th); got != want {
					t.Errorf("detail line = %q, want %q", got, want)
				}
				if line, want := serverLine(got, th, 240), compactMetricsHint(original); !strings.Contains(line, want) {
					t.Errorf("compact line = %q, want hint %q", line, want)
				}
				if s.Servers[0].Kind != kind {
					t.Fatal("caller-owned engine kind changed")
				}
			})
		}
	}
}

// All free-form strings reach the scrub's maximum input length, with a
// slow address-like shape. The complete JSON still fits the client's 8 MiB
// body bound: eight GPUs, 128 processes and 256 servers on a busy host.
func maximumFieldSnapshot() *model.Snapshot {
	text := "192.0.2.10 " + strings.Repeat("1:1:1:1:1:1:1:1x ", maxScrubLen/16+1)
	text = text[:maxScrubLen]
	s := &model.Snapshot{
		Host:      model.HostInfo{Name: text, Hostname: text, DriverVersion: text, AgentVersion: text},
		Timestamp: sampleTS,
	}
	for i := 0; i < 8; i++ {
		s.GPUs = append(s.GPUs, model.GPU{Index: i, UUID: text, Name: text})
		s.Errors = append(s.Errors, text)
	}
	for i := 0; i < 128; i++ {
		s.Procs = append(s.Procs, model.GPUProc{PID: int32(i + 1), GPUIndex: i % 8, Name: text, Class: model.ProcLLM})
	}
	for i := 0; i < 256; i++ {
		s.Servers = append(s.Servers, model.LLMServer{
			Kind: text, Endpoint: text, Role: text, Status: text, Model: text, MetricsErr: text,
			PID: int32(i%128 + 1), GPUs: []int{i % 8},
		})
	}
	return s
}

func TestHiddenAddressesMaximumFieldSnapshot(t *testing.T) {
	for _, errors := range []bool{false, true} {
		t.Run(fmt.Sprintf("errors=%v", errors), func(t *testing.T) {
			s := maximumFieldSnapshot()
			if !errors {
				s.Errors = nil
			}
			body, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			if len(body) >= 8<<20 {
				t.Fatalf("fixture exceeds client body bound: %d bytes", len(body))
			}
			h := NewHostState("alpha")
			h.view.showAddrs = false
			start := time.Now()
			h.Apply(client.Update{Host: "alpha", Snapshot: s})
			elapsed := time.Since(start)
			t.Logf("Apply: %v; JSON body: %d bytes", elapsed, len(body))
			if elapsed > time.Second {
				t.Errorf("Apply took %v", elapsed)
			}
			texts := append([]string{h.Snap.Host.DriverVersion}, h.Snap.Errors...)
			for _, g := range h.Snap.GPUs {
				texts = append(texts, g.Name)
			}
			for _, p := range h.Snap.Procs {
				texts = append(texts, p.Name)
			}
			for _, srv := range h.Snap.Servers {
				texts = append(texts, srv.Model, srv.Kind, srv.Status, srv.Role, srv.MetricsErr)
				if srv.Endpoint != "…" {
					t.Errorf("endpoint = %q", srv.Endpoint)
				}
			}
			kept := 0
			for _, text := range texts {
				if strings.Contains(text, "192.0.2.10") {
					t.Fatal("address survived in a displayed field")
				}
				if text != "…" {
					kept++
				}
			}
			if want := scrubBudget / maxScrubLen; kept != want {
				t.Errorf("%d full-length fields processed, want shared budget of %d", kept, want)
			}
		})
	}
}

func BenchmarkHiddenAddressesMaximumFieldSnapshot(b *testing.B) {
	s := maximumFieldSnapshot()
	// Without errors, the budget is spent on the newly covered fields.
	s.Errors = nil
	v := hostView{showGraphics: true}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = v.filterSnapshot(s)
	}
}

// TestScrubErrCutKeepsRedactStringsWhole cuts long text made of the
// host's configured URL at every offset. Striking the URL shortens the
// text enough that the cut's neighbourhood reaches the screen, so no part
// of the URL may be left there, scheme included.
func TestScrubErrCutKeepsRedactStringsWhole(t *testing.T) {
	v := hostStates(ViewConfig{
		Hosts:             []HostSpec{{Name: "alpha", Addr: "https://gpu.example.com:9832/"}},
		ShowAddresses:     false,
		ShowHostnames:     true,
		ShowGraphicsProcs: true,
	})[0].view
	const unit = "at https://gpu.example.com:9832/ "
	long := strings.Repeat(unit, 2*maxScrubLen/len(unit)+2)
	for pad := 0; pad < len(unit); pad++ {
		got := v.scrubErr(strings.Repeat("y", pad) + long)
		for _, leak := range []string{"http", "gpu", "example", "9832"} {
			if strings.Contains(got, leak) {
				t.Fatalf("pad %d: %q shown, ends ...%q", pad, leak, got[max(0, len(got)-40):])
			}
		}
	}
}
