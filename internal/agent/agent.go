// Package agent is the print agent's work: pair with a restaurant, then claim
// print jobs, print them and report back, for as long as it runs.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/quicktable/print-agent/internal/api"
	"github.com/quicktable/print-agent/internal/escpos"
	"github.com/quicktable/print-agent/internal/update"
)

// API is the part of the QuickTable API the agent uses (see api.Client).
type API interface {
	ClaimInstall(ctx context.Context, code string) (token string, err error)
	Heartbeat(ctx context.Context, printers []api.ReportedPrinter, failure *api.UpdateFailure) (api.HeartbeatResult, error)
	Claim(ctx context.Context, wait time.Duration) (jobs []api.Job, retryAfter time.Duration, err error)
	Report(ctx context.Context, jobID string, printErr error) error
	SendDiagnostics(ctx context.Context, log string) error
}

// Agent wires the API to the PC's printers. Every field is required unless
// noted; the durations have defaults.
type Agent struct {
	// Connect returns an API client using the given token ("" before pairing).
	Connect func(token string) API
	// LoadToken and SaveToken keep the pairing token between runs ("" = unpaired).
	LoadToken func() string
	SaveToken func(token string) error
	// Send prints data on a printer (see transport.Send).
	Send func(ctx context.Context, connection, address string, data []byte) error
	// InstalledPrinters lists the printers Windows has (see transport.InstalledPrinters).
	InstalledPrinters func() ([]string, error)
	// InstallCode is the code the downloaded program carried in its file name
	// (an install started in the admin), "" when there is none;
	// ClearInstallCode forgets it once it was tried.
	InstallCode      func() string
	ClearInstallCode func() error
	// AskCode asks the person to type the code the admin shows. It blocks
	// until they answer; false means "not now". WrongCode tells them the code
	// they typed isn't good, ConnectRequested waits until they ask to connect
	// after a "not now" (false when ctx ended first), and Paired tells them
	// it worked.
	AskCode          func(ctx context.Context) (code string, ok bool)
	WrongCode        func()
	ConnectRequested func(ctx context.Context) bool
	Paired           func()
	// Status (optional) is told how the agent is doing, whenever it changes or not.
	Status func(State)
	Log    *slog.Logger

	// ApplyUpdate (optional: without it the agent never updates) replaces the
	// running program with the release. When it works it doesn't return — the
	// new version takes over; an error means the agent carries on as it is.
	ApplyUpdate func(ctx context.Context, release update.Release) error
	// FailedUpdate (optional) is the update that was tried and undone, if
	// any: it is reported to the API and not tried again.
	FailedUpdate func() *api.UpdateFailure
	// Diagnostics (optional) is the end of the agent's own log: sent to the
	// API when QuickTable asks for it in a heartbeat's answer.
	Diagnostics func() (string, error)
	// Now is the PC's clock (time.Now by default).
	Now func() time.Time

	// ClaimWait is how long a claim with nothing to print is held open (the API caps it).
	ClaimWait time.Duration
	// HeartbeatEvery is the heartbeat's period.
	HeartbeatEvery time.Duration
	// Sleep waits, or returns early (false) when ctx ends. Replaced in tests.
	Sleep func(ctx context.Context, d time.Duration) bool

	startedAt time.Time
}

// State is how the agent is doing, as the person at the PC needs to know it.
type State int

const (
	// Unpaired: not connected to any restaurant.
	Unpaired State = iota
	// Connected: paired, and the API answers.
	Connected
	// Offline: the API can't be reached; it keeps trying.
	Offline
)

const (
	defaultClaimWait      = 25 * time.Second
	defaultHeartbeatEvery = 60 * time.Second
	// After a failed request: this long, doubling up to the maximum.
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
	// A result that couldn't be reported is retried this many times; after
	// that the job's lease runs out and the API hands it out again.
	reportAttempts = 3
	// An agent that just started updates right away, whatever the hour:
	// starting is already an interruption, and restarting the agent (or the
	// PC) is how someone at the restaurant forces an update.
	startupUpdateGrace = 3 * time.Minute
)

func (a *Agent) defaults() {
	if a.ClaimWait == 0 {
		a.ClaimWait = defaultClaimWait
	}
	if a.HeartbeatEvery == 0 {
		a.HeartbeatEvery = defaultHeartbeatEvery
	}
	if a.Sleep == nil {
		a.Sleep = sleep
	}
	if a.Now == nil {
		a.Now = time.Now
	}
	a.startedAt = a.Now()
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Run pairs if needed and works until ctx ends. An agent that gets unpaired
// from the admin goes back to showing a pairing code.
func (a *Agent) Run(ctx context.Context) error {
	a.defaults()
	for ctx.Err() == nil {
		token := a.LoadToken()
		if token == "" {
			var err error
			if token, err = a.pair(ctx); err != nil {
				return err
			}
		}
		release, err := a.work(ctx, a.Connect(token))
		if errors.Is(err, api.ErrUnauthorized) {
			a.Log.Warn("unpaired from the admin: pairing again")
			if err := a.SaveToken(""); err != nil {
				return fmt.Errorf("forgetting the pairing: %w", err)
			}
		}
		if release != nil {
			a.Log.Info("updating", "version", release.Version)
			if err := a.ApplyUpdate(ctx, *release); err != nil {
				a.Log.Error("the update was not applied", "version", release.Version, "err", err)
			}
		}
	}
	return ctx.Err()
}

// pair gets the agent its token, already saved, by trading an install's code
// for it. The code normally came in the downloaded program's file name and
// nobody sees it. Without one — the file lost its name, or this PC was
// removed from the admin — the person is asked to type the one the admin
// shows; if they'd rather not now, the agent waits until they ask to connect.
func (a *Agent) pair(ctx context.Context) (string, error) {
	client := a.Connect("")
	if code := a.InstallCode(); code != "" {
		token, err := a.claim(ctx, client, code)
		if clearErr := a.ClearInstallCode(); clearErr != nil {
			return "", fmt.Errorf("saving the pairing: %w", clearErr)
		}
		switch {
		case err == nil:
			return token, a.paired(token)
		case ctx.Err() != nil:
			return "", ctx.Err()
		}
		a.Log.Warn("the install code is no longer valid: asking for one")
	}

	a.setStatus(Unpaired)
	for ask := true; ; ask = false {
		// Asked once on its own; after a "not now", when the person says so.
		if !ask && !a.ConnectRequested(ctx) {
			return "", ctx.Err()
		}
		for {
			code, ok := a.AskCode(ctx)
			if !ok {
				break
			}
			token, err := a.claim(ctx, client, code)
			if err == nil {
				return token, a.paired(token)
			}
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			a.WrongCode()
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
}

// claim trades a code for the token, waiting out a connection that is down.
// Its error is api.ErrNotFound for a code that isn't good (anymore), or ctx's.
func (a *Agent) claim(ctx context.Context, client API, code string) (string, error) {
	backoff := minBackoff
	for {
		token, err := client.ClaimInstall(ctx, code)
		if err == nil || errors.Is(err, api.ErrNotFound) {
			return token, err
		}
		a.Log.Warn("could not reach the API to connect", "err", err)
		a.setStatus(Offline)
		if !a.Sleep(ctx, backoff) {
			return "", ctx.Err()
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func (a *Agent) paired(token string) error {
	if err := a.SaveToken(token); err != nil {
		return fmt.Errorf("saving the pairing: %w", err)
	}
	a.Log.Info("paired")
	a.Paired()
	return nil
}

// setStatus tells whoever shows it (the tray icon) how the agent is doing.
func (a *Agent) setStatus(state State) {
	if a.Status != nil {
		a.Status(state)
	}
}

// work claims and prints until ctx ends, the API stops accepting the token
// (the error), or an update is due and nothing is waiting to print (the
// release).
func (a *Agent) work(ctx context.Context, client API) (*update.Release, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	updates := make(chan update.Release, 1)
	go a.heartbeats(ctx, client, updates)

	backoff := minBackoff
	for ctx.Err() == nil {
		jobs, retryAfter, err := client.Claim(ctx, a.ClaimWait)
		if errors.Is(err, api.ErrUnauthorized) {
			return nil, err
		}
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			a.Log.Warn("could not ask for jobs", "err", err)
			a.setStatus(Offline)
			if !a.Sleep(ctx, backoff) {
				break
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		backoff = minBackoff
		a.setStatus(Connected)
		if unauthorized := a.printAll(ctx, client, jobs); unauthorized {
			return nil, api.ErrUnauthorized
		}
		// Updating takes the agent away for a few seconds: only with the
		// queue empty.
		if len(jobs) == 0 {
			select {
			case release := <-updates:
				return &release, nil
			default:
			}
		}
		// Nothing going on at the restaurant: the API said when to come back.
		if retryAfter > 0 && !a.Sleep(ctx, retryAfter) {
			break
		}
	}
	return nil, ctx.Err()
}

// heartbeats reports the agent alive, with the PC's printers, now and then
// every HeartbeatEvery, until ctx ends. A release the API says to update to
// goes to updates once it's time to apply it.
func (a *Agent) heartbeats(ctx context.Context, client API, updates chan<- update.Release) {
	for {
		names, err := a.InstalledPrinters()
		if err != nil {
			a.Log.Warn("could not list the PC's printers", "err", err)
		}
		printers := make([]api.ReportedPrinter, 0, len(names))
		for _, name := range names {
			printers = append(printers, api.ReportedPrinter{Name: name})
		}
		// Without the list, nothing is reported rather than "no printers":
		// that would make the API forget them.
		if err == nil {
			var failure *api.UpdateFailure
			if a.FailedUpdate != nil {
				failure = a.FailedUpdate()
			}
			result, err := client.Heartbeat(ctx, printers, failure)
			if err == nil && result.DiagnosticsRequested {
				a.sendDiagnostics(ctx, client)
			}
			switch {
			case err != nil && ctx.Err() == nil:
				a.Log.Warn("heartbeat failed", "err", err)
			case err == nil && a.updateDue(result, failure):
				// One release waits at most: the newest answer replaces it.
				select {
				case updates <- *result.Update:
				default:
				}
			}
		}
		if !a.Sleep(ctx, a.HeartbeatEvery) {
			return
		}
	}
}

// sendDiagnostics uploads the log QuickTable asked for. A failure is only
// logged: the API keeps asking until it gets it.
func (a *Agent) sendDiagnostics(ctx context.Context, client API) {
	if a.Diagnostics == nil {
		return
	}
	text, err := a.Diagnostics()
	if err == nil {
		err = client.SendDiagnostics(ctx, text)
	}
	if err != nil {
		a.Log.Warn("could not send the diagnostics", "err", err)
		return
	}
	a.Log.Info("diagnostics sent", "asked", "quicktable")
}

// printAll prints the claimed jobs: printers work side by side, each one's
// jobs in order. It reports whether the API rejected the agent's token.
func (a *Agent) printAll(ctx context.Context, client API, jobs []api.Job) (unauthorized bool) {
	var order []string
	byPrinter := map[string][]api.Job{}
	for _, job := range jobs {
		if _, seen := byPrinter[job.Printer.ID]; !seen {
			order = append(order, job.Printer.ID)
		}
		byPrinter[job.Printer.ID] = append(byPrinter[job.Printer.ID], job)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, printerID := range order {
		wg.Add(1)
		go func(jobs []api.Job) {
			defer wg.Done()
			// A printer that just failed isn't tried again for the rest of
			// its batch: each attempt costs seconds of the jobs' lease.
			var down error
			for _, job := range jobs {
				err := down
				if err == nil {
					err = a.print(ctx, job)
					down = err
				}
				if a.report(ctx, client, job, err) {
					mu.Lock()
					unauthorized = true
					mu.Unlock()
				}
			}
		}(byPrinter[printerID])
	}
	wg.Wait()
	return unauthorized
}

func (a *Agent) print(ctx context.Context, job api.Job) error {
	data := escpos.Render(job.Ticket, escpos.Options{
		PaperWidthMm: job.Printer.PaperWidthMm,
		Reprint:      job.Reprint,
	})
	copies := max(job.Copies, 1)
	for range copies {
		if err := a.Send(ctx, job.Printer.Connection, job.Printer.Address, data); err != nil {
			return err
		}
	}
	return nil
}

// report tells the API how the job went. It returns true when the API
// rejected the agent's token.
func (a *Agent) report(ctx context.Context, client API, job api.Job, printErr error) (unauthorized bool) {
	log := a.Log.With("job", job.ID, "printer", job.Printer.Name, "attempt", job.Attempt)
	if printErr != nil {
		log.Warn("ticket not printed", "err", printErr)
	} else {
		log.Info("ticket printed")
	}
	for attempt := 1; ; attempt++ {
		err := client.Report(ctx, job.ID, printErr)
		switch {
		case err == nil:
			return false
		case errors.Is(err, api.ErrUnauthorized):
			return true
		case errors.Is(err, api.ErrNotHeld):
			log.Warn("the job was no longer this agent's when it finished")
			return false
		}
		log.Warn("could not report the result", "err", err)
		if attempt == reportAttempts || !a.Sleep(ctx, minBackoff) {
			return false
		}
	}
}

// updateDue reports whether the heartbeat's release should be applied now:
// there is one, it isn't the one that already failed, and it is either the
// agent's first minutes running or the restaurant's update window.
func (a *Agent) updateDue(result api.HeartbeatResult, failed *api.UpdateFailure) bool {
	if a.ApplyUpdate == nil || result.Update == nil {
		return false
	}
	if failed != nil && failed.Version == result.Update.Version {
		return false
	}
	now := a.Now()
	if now.Sub(a.startedAt) < startupUpdateGrace {
		return true
	}
	window := result.UpdateWindow
	return update.InWindow(now.Hour()*60+now.Minute(), window.StartMinute, window.EndMinute)
}
