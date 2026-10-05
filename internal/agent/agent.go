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
	StartPairing(ctx context.Context) (api.Pairing, error)
	PollPairing(ctx context.Context, deviceCode string) (api.PairingStatus, error)
	Heartbeat(ctx context.Context, printers []api.ReportedPrinter, failure *api.UpdateFailure) (api.HeartbeatResult, error)
	Claim(ctx context.Context, wait time.Duration) ([]api.Job, error)
	Report(ctx context.Context, jobID string, printErr error) error
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
	// InstallCode is the code the downloaded program carried (an install
	// approved in advance), "" when there is none; ClearInstallCode forgets it
	// once it was used or turned out to be no good.
	InstallCode      func() string
	ClearInstallCode func() error
	// Unpaired tells the person this PC isn't connected to a restaurant and
	// how to connect it. CodeRequested then waits until they ask to connect
	// with a code (false when ctx ended first), ShowCode shows them the code
	// to enter in the admin, and Paired tells them it worked. Only
	// CodeRequested may block.
	Unpaired      func()
	CodeRequested func(ctx context.Context) bool
	ShowCode      func(userCode string)
	Paired        func()
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

// pair gets the agent its token, already saved. The normal way needs nobody:
// the downloaded program carried an install code, approved in advance, and
// it is traded for the token. Without one the agent waits, unpaired, until
// the person asks to connect with a code — the fallback — and then shows one
// and waits for a manager to approve it.
func (a *Agent) pair(ctx context.Context) (string, error) {
	client := a.Connect("")
	if code := a.InstallCode(); code != "" {
		token, err := a.claimInstall(ctx, client, code)
		if err != nil || token != "" {
			return token, err
		}
	}

	a.setStatus(Unpaired)
	a.Unpaired()
	backoff := minBackoff
	for {
		if !a.CodeRequested(ctx) {
			return "", ctx.Err()
		}
		pairing, err := client.StartPairing(ctx)
		if err != nil {
			a.Log.Warn("could not start pairing", "err", err)
			if !a.Sleep(ctx, backoff) {
				return "", ctx.Err()
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		backoff = minBackoff
		a.Log.Info("pairing started: waiting for the code to be approved in the admin")
		a.ShowCode(pairing.UserCode)

		token, err := a.awaitApproval(ctx, client, pairing)
		if err != nil {
			return "", err
		}
		if token == "" {
			// No new code on its own: nobody may be there to read it.
			a.Log.Info("the pairing code expired")
			continue
		}
		return token, a.paired(token)
	}
}

// claimInstall trades the install code for the token. It returns "" when the
// code is no good (expired, or its install was replaced by a newer one): the
// code is then forgotten.
func (a *Agent) claimInstall(ctx context.Context, client API, code string) (string, error) {
	backoff := minBackoff
	for {
		status, err := client.PollPairing(ctx, code)
		switch {
		case err == nil && status.Status == "approved" && status.Token != "":
			if err := a.ClearInstallCode(); err != nil {
				return "", fmt.Errorf("saving the pairing: %w", err)
			}
			return status.Token, a.paired(status.Token)
		case err == nil || errors.Is(err, api.ErrNotFound):
			a.Log.Warn("the install code is no longer valid: waiting to be connected")
			return "", a.ClearInstallCode()
		}
		// Offline for now: the code is good for a while.
		a.Log.Warn("could not connect with the install code", "err", err)
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

// awaitApproval polls until the code is approved (the token) or stops being
// usable ("").
func (a *Agent) awaitApproval(ctx context.Context, client API, pairing api.Pairing) (string, error) {
	interval := time.Duration(pairing.PollIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 3 * time.Second
	}
	deadline := time.Now().Add(time.Duration(pairing.ExpiresInSeconds) * time.Second)
	for {
		if !a.Sleep(ctx, interval) {
			return "", ctx.Err()
		}
		status, err := client.PollPairing(ctx, pairing.DeviceCode)
		switch {
		case err != nil:
			// Offline for a moment: the code is still good until it expires.
			a.Log.Warn("could not check the pairing", "err", err)
			if pairing.ExpiresInSeconds > 0 && time.Now().After(deadline) {
				return "", nil
			}
		case status.Status == "approved" && status.Token != "":
			return status.Token, nil
		case status.Status != "pending":
			return "", nil
		}
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
		jobs, err := client.Claim(ctx, a.ClaimWait)
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
