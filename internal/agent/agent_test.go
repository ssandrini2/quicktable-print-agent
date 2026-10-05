package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quicktable/print-agent/internal/api"
	"github.com/quicktable/print-agent/internal/escpos"
	"github.com/quicktable/print-agent/internal/update"
)

// fakeAPI scripts the API's answers and records what the agent sent.
type fakeAPI struct {
	mu sync.Mutex

	token        string
	pairings     []api.Pairing       // StartPairing answers, in order
	polls        []api.PairingStatus // PollPairing answers, in order
	claims       [][]api.Job         // Claim answers, in order; then onIdle
	onIdle       func()              // called once the scripted claims ran out
	claimErrs    []error             // returned (one per call) before the claims
	reportErrs   map[string][]error  // per job: errors before a report is accepted
	reports      map[string][]error  // per job: the print errors reported
	heartbeats   [][]api.ReportedPrinter
	startedPairs int

	heartbeat  api.HeartbeatResult // what every Heartbeat answers
	failures   []*api.UpdateFailure
	idleClaims int // empty claims answered (after the first heartbeat) before onIdle ends the run
	beat       sync.Once
	beaten     chan struct{}
}

func (f *fakeAPI) StartPairing(context.Context) (api.Pairing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pairing := f.pairings[f.startedPairs]
	f.startedPairs++
	return pairing, nil
}

func (f *fakeAPI) PollPairing(context.Context, string) (api.PairingStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	status := f.polls[0]
	f.polls = f.polls[1:]
	return status, nil
}

func (f *fakeAPI) Heartbeat(_ context.Context, printers []api.ReportedPrinter, failure *api.UpdateFailure) (api.HeartbeatResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heartbeats = append(f.heartbeats, printers)
	f.failures = append(f.failures, failure)
	f.beat.Do(func() { close(f.beaten) })
	return f.heartbeat, nil
}

func (f *fakeAPI) Claim(ctx context.Context, _ time.Duration) ([]api.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.claimErrs) > 0 {
		err := f.claimErrs[0]
		f.claimErrs = f.claimErrs[1:]
		return nil, err
	}
	if len(f.claims) == 0 {
		if f.idleClaims > 0 {
			// Give the heartbeat's answer time to be acted on.
			f.mu.Unlock()
			<-f.beaten
			time.Sleep(20 * time.Millisecond)
			f.mu.Lock()
			f.idleClaims--
			return nil, nil
		}
		if f.onIdle != nil {
			f.onIdle()
		}
		return nil, ctx.Err()
	}
	jobs := f.claims[0]
	f.claims = f.claims[1:]
	return jobs, nil
}

func (f *fakeAPI) Report(_ context.Context, jobID string, printErr error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if pending := f.reportErrs[jobID]; len(pending) > 0 {
		f.reportErrs[jobID] = pending[1:]
		return pending[0]
	}
	if f.reports == nil {
		f.reports = map[string][]error{}
	}
	f.reports[jobID] = append(f.reports[jobID], printErr)
	return nil
}

type sent struct {
	connection, address string
	data                []byte
}

// harness is an agent over a fake API and fake printers; Run ends once the
// scripted claims ran out.
type harness struct {
	t       *testing.T
	api     *fakeAPI
	agent   *Agent
	mu      sync.Mutex
	token   string
	sent    []sent
	offline map[string]bool // printer addresses that fail
	codes   []string
	// The code the downloaded program carried, and what became of the wait for someone to ask for one.
	installCode string
	nobodyAsks  bool
	unpaired    int
	states      []State
	paired      int
	tokens      []string // tokens the agent connected with
	cancel      context.CancelFunc
}

func newHarness(t *testing.T, token string) *harness {
	h := &harness{t: t, api: &fakeAPI{beaten: make(chan struct{})}, token: token, offline: map[string]bool{}}
	h.agent = &Agent{
		Connect: func(token string) API {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.tokens = append(h.tokens, token)
			return h.api
		},
		LoadToken: func() string {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.token
		},
		SaveToken: func(token string) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.token = token
			return nil
		},
		Send: func(_ context.Context, connection, address string, data []byte) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.offline[address] {
				return errors.New("printer offline")
			}
			h.sent = append(h.sent, sent{connection, address, data})
			return nil
		},
		InstalledPrinters: func() ([]string, error) { return []string{"EPSON TM-T20"}, nil },
		InstallCode: func() string {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.installCode
		},
		ClearInstallCode: func() error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.installCode = ""
			return nil
		},
		Unpaired: func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.unpaired++
		},
		// Someone asks for a code right away, unless the test says nobody does.
		CodeRequested: func(ctx context.Context) bool {
			if h.nobodyAsks {
				<-ctx.Done()
			}
			return ctx.Err() == nil
		},
		Status: func(state State) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.states = append(h.states, state)
		},
		ShowCode: func(code string) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.codes = append(h.codes, code)
		},
		Paired: func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.paired++
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		// No real waiting: a sleep only checks whether the run is over.
		Sleep: func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil },
	}
	return h
}

func (h *harness) run() {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h.api.onIdle = cancel
	if err := h.agent.Run(ctx); !errors.Is(err, context.Canceled) {
		h.t.Fatalf("Run ended with %v", err)
	}
}

func job(id, printerID, address string) api.Job {
	return api.Job{
		ID:      id,
		Attempt: 1,
		Copies:  1,
		Printer: api.Printer{ID: printerID, Name: printerID, Connection: "NETWORK", Address: address, PaperWidthMm: 80},
		Ticket:  escpos.Ticket{Kind: "order", Title: "Cocina", Lines: []escpos.Line{{Quantity: 1, Name: id}}},
	}
}

func TestPrintsClaimedJobsAndReportsThem(t *testing.T) {
	h := newHarness(t, "token")
	twoCopies := job("b", "bar", "10.0.0.2:9100")
	twoCopies.Copies = 2
	twoCopies.Reprint = true
	h.api.claims = [][]api.Job{{job("a", "kitchen", "10.0.0.1:9100"), twoCopies}}

	h.run()

	if len(h.sent) != 3 {
		t.Fatalf("sent %d tickets, want 3 (one, plus two copies)", len(h.sent))
	}
	for _, s := range h.sent {
		marked := bytes.Contains(s.data, []byte("*** REIMPRESI"))
		if marked != (s.address == "10.0.0.2:9100") {
			t.Errorf("reprint mark on %s: %v", s.address, marked)
		}
	}
	for _, id := range []string{"a", "b"} {
		if got := h.api.reports[id]; len(got) != 1 || got[0] != nil {
			t.Errorf("job %s reported %v, want printed once", id, got)
		}
	}
	if len(h.api.heartbeats) == 0 || h.api.heartbeats[0][0].Name != "EPSON TM-T20" {
		t.Errorf("heartbeats: %v", h.api.heartbeats)
	}
	if len(h.codes) != 0 {
		t.Error("a paired agent must not show a pairing code")
	}
}

func TestAPrinterThatIsDownFailsItsJobsOnly(t *testing.T) {
	h := newHarness(t, "token")
	h.offline["10.0.0.1:9100"] = true
	h.api.claims = [][]api.Job{{
		job("a", "kitchen", "10.0.0.1:9100"),
		job("b", "kitchen", "10.0.0.1:9100"),
		job("c", "bar", "10.0.0.2:9100"),
	}}

	h.run()

	for _, id := range []string{"a", "b"} {
		if got := h.api.reports[id]; len(got) != 1 || got[0] == nil {
			t.Errorf("job %s reported %v, want failed", id, got)
		}
	}
	if got := h.api.reports["c"]; len(got) != 1 || got[0] != nil {
		t.Errorf("job c reported %v, want printed", got)
	}
	if len(h.sent) != 1 {
		t.Errorf("sent %d tickets, want 1", len(h.sent))
	}
}

func TestKeepsGoingAfterRequestsFail(t *testing.T) {
	h := newHarness(t, "token")
	h.api.claimErrs = []error{errors.New("no network"), errors.New("no network")}
	h.api.reportErrs = map[string][]error{"a": {errors.New("timeout")}, "b": {api.ErrNotHeld}}
	h.api.claims = [][]api.Job{{job("a", "kitchen", "10.0.0.1:9100")}, {job("b", "kitchen", "10.0.0.1:9100")}}

	h.run()

	if got := h.api.reports["a"]; len(got) != 1 {
		t.Errorf("job a: %d accepted reports, want 1 (after a retry)", len(got))
	}
	if got := h.api.reports["b"]; len(got) != 0 {
		t.Errorf("job b: a job that isn't the agent's anymore is not reported again, got %v", got)
	}
	if len(h.sent) != 2 {
		t.Errorf("sent %d tickets, want 2", len(h.sent))
	}
}

func TestPairsFirstAndAsksForANewCodeWhenOneExpires(t *testing.T) {
	h := newHarness(t, "")
	h.api.pairings = []api.Pairing{
		{DeviceCode: "d1", UserCode: "AAAA2222", ExpiresInSeconds: 600, PollIntervalSeconds: 3},
		{DeviceCode: "d2", UserCode: "BBBB3333", ExpiresInSeconds: 600, PollIntervalSeconds: 3},
	}
	h.api.polls = []api.PairingStatus{
		{Status: "pending"},
		{Status: "expired"},
		{Status: "pending"},
		{Status: "approved", Token: "fresh-token"},
	}
	h.api.claims = [][]api.Job{{job("a", "kitchen", "10.0.0.1:9100")}}

	h.run()

	if len(h.codes) != 2 || h.codes[0] != "AAAA2222" || h.codes[1] != "BBBB3333" {
		t.Fatalf("codes shown: %v", h.codes)
	}
	if h.token != "fresh-token" || h.paired != 1 {
		t.Fatalf("token %q, paired %d", h.token, h.paired)
	}
	if last := h.tokens[len(h.tokens)-1]; last != "fresh-token" {
		t.Errorf("the agent works with %q", last)
	}
	if len(h.sent) != 1 {
		t.Errorf("sent %d tickets, want 1", len(h.sent))
	}
}

func TestPairsAgainWhenUnpairedFromTheAdmin(t *testing.T) {
	h := newHarness(t, "revoked-token")
	h.api.claimErrs = []error{api.ErrUnauthorized}
	h.api.pairings = []api.Pairing{{DeviceCode: "d1", UserCode: "AAAA2222", ExpiresInSeconds: 600, PollIntervalSeconds: 3}}
	h.api.polls = []api.PairingStatus{{Status: "approved", Token: "fresh-token"}}

	h.run()

	if h.token != "fresh-token" || len(h.codes) != 1 {
		t.Fatalf("token %q, codes %v", h.token, h.codes)
	}
}

var release = update.Release{Version: "1.1.0", URL: "https://example.test/agent.exe", SHA256: "abc"}

// At 15:00, outside the 03:00-06:00 window.
var afternoon = time.Date(2026, 10, 1, 15, 0, 0, 0, time.Local)

func TestUpdatesOnceTheQueueIsEmpty(t *testing.T) {
	h := newHarness(t, "token")
	h.api.heartbeat = api.HeartbeatResult{Update: &release, UpdateWindow: api.UpdateWindow{StartMinute: 180, EndMinute: 360}}
	h.api.idleClaims = 50
	var applied []string
	h.agent.Now = func() time.Time { return afternoon }
	h.agent.ApplyUpdate = func(_ context.Context, r update.Release) error {
		applied = append(applied, r.Version)
		h.api.mu.Lock()
		h.api.idleClaims = 0
		h.api.heartbeat = api.HeartbeatResult{}
		h.api.mu.Unlock()
		return errors.New("stay as you are")
	}

	h.run()

	// Just started: it updates whatever the hour.
	if len(applied) != 1 || applied[0] != "1.1.0" {
		t.Fatalf("applied %v", applied)
	}
}

func TestWaitsForTheUpdateWindow(t *testing.T) {
	for _, c := range []struct {
		name string
		now  time.Time
		want int
	}{
		{"outside the window", afternoon, 0},
		{"inside the window", time.Date(2026, 10, 1, 4, 0, 0, 0, time.Local), 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, "token")
			h.api.heartbeat = api.HeartbeatResult{Update: &release, UpdateWindow: api.UpdateWindow{StartMinute: 180, EndMinute: 360}}
			h.api.idleClaims = 3
			applied := 0
			// The first reading is when it started; by the heartbeat it has been running for an hour.
			var readings atomic.Int32
			h.agent.Now = func() time.Time {
				if readings.Add(1) == 1 {
					return c.now.Add(-time.Hour)
				}
				return c.now
			}
			h.agent.ApplyUpdate = func(context.Context, update.Release) error {
				applied++
				h.api.mu.Lock()
				h.api.heartbeat = api.HeartbeatResult{}
				h.api.mu.Unlock()
				return errors.New("stay as you are")
			}

			h.run()

			if applied != c.want {
				t.Fatalf("applied %d times, want %d", applied, c.want)
			}
		})
	}
}

func TestReportsAFailedUpdateAndDoesNotRetryIt(t *testing.T) {
	h := newHarness(t, "token")
	h.api.heartbeat = api.HeartbeatResult{Update: &release}
	h.api.idleClaims = 3
	h.agent.Now = func() time.Time { return afternoon }
	h.agent.FailedUpdate = func() *api.UpdateFailure {
		return &api.UpdateFailure{Version: "1.1.0", Error: "it did not start"}
	}
	h.agent.ApplyUpdate = func(context.Context, update.Release) error {
		t.Error("a version that already failed must not be tried again")
		return nil
	}

	h.run()

	if len(h.api.failures) == 0 || h.api.failures[0] == nil || h.api.failures[0].Version != "1.1.0" {
		t.Fatalf("reported %v", h.api.failures)
	}
}

func TestInstallCodePairsWithoutShowingACode(t *testing.T) {
	h := newHarness(t, "")
	h.installCode = "the-code-in-the-file-name"
	h.api.polls = []api.PairingStatus{{Status: "approved", Token: "fresh-token"}}
	h.api.claims = [][]api.Job{{job("a", "kitchen", "10.0.0.1:9100")}}

	h.run()

	if h.token != "fresh-token" || h.installCode != "" || h.paired != 1 {
		t.Fatalf("token %q, install code %q, paired %d", h.token, h.installCode, h.paired)
	}
	if len(h.codes) != 0 || h.unpaired != 0 {
		t.Errorf("nobody should be asked anything: codes %v, unpaired notices %d", h.codes, h.unpaired)
	}
	if len(h.sent) != 1 || h.states[len(h.states)-1] != Connected {
		t.Errorf("sent %d tickets, states %v", len(h.sent), h.states)
	}
}

func TestAnInstallCodeThatExpiredLeavesTheAgentWaiting(t *testing.T) {
	h := newHarness(t, "")
	h.installCode = "stale"
	h.nobodyAsks = true
	h.api.polls = []api.PairingStatus{{Status: "expired"}}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_ = h.agent.Run(ctx)

	if h.installCode != "" || h.token != "" {
		t.Fatalf("install code %q, token %q", h.installCode, h.token)
	}
	// It says so once and shows no code until someone asks for one.
	if h.unpaired != 1 || len(h.codes) != 0 || h.api.startedPairs != 0 {
		t.Errorf("unpaired notices %d, codes %v, pairings started %d", h.unpaired, h.codes, h.api.startedPairs)
	}
	if len(h.states) == 0 || h.states[len(h.states)-1] != Unpaired {
		t.Errorf("states %v", h.states)
	}
}
