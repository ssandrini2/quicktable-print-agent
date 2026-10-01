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
)

// API is the part of the QuickTable API the agent uses (see api.Client).
type API interface {
	StartPairing(ctx context.Context) (api.Pairing, error)
	PollPairing(ctx context.Context, deviceCode string) (api.PairingStatus, error)
	Heartbeat(ctx context.Context, printers []api.ReportedPrinter) error
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
	// BeforeCode (optional) waits until a new pairing code can be shown — the
	// notice of the previous one was closed. False when ctx ended first.
	BeforeCode func(ctx context.Context) bool
	// ShowCode tells the person at the PC the code to enter in the admin;
	// Paired tells them it worked. Neither may block.
	ShowCode func(userCode string)
	Paired   func()
	Log      *slog.Logger

	// ClaimWait is how long a claim with nothing to print is held open (the API caps it).
	ClaimWait time.Duration
	// HeartbeatEvery is the heartbeat's period.
	HeartbeatEvery time.Duration
	// Sleep waits, or returns early (false) when ctx ends. Replaced in tests.
	Sleep func(ctx context.Context, d time.Duration) bool
}

const (
	defaultClaimWait      = 25 * time.Second
	defaultHeartbeatEvery = 60 * time.Second
	// After a failed request: this long, doubling up to the maximum.
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
	// A result that couldn't be reported is retried this many times; after
	// that the job's lease runs out and the API hands it out again.
	reportAttempts = 3
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
		if err := a.work(ctx, a.Connect(token)); errors.Is(err, api.ErrUnauthorized) {
			a.Log.Warn("unpaired from the admin: pairing again")
			if err := a.SaveToken(""); err != nil {
				return fmt.Errorf("forgetting the pairing: %w", err)
			}
		}
	}
	return ctx.Err()
}

// pair shows a code and waits until a manager approves it, asking for a new
// code whenever one expires. It returns the token, already saved.
func (a *Agent) pair(ctx context.Context) (string, error) {
	client := a.Connect("")
	backoff := minBackoff
	for {
		// Nobody at the PC means no new codes: they'd pile up unread.
		if a.BeforeCode != nil && !a.BeforeCode(ctx) {
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
			a.Log.Info("the pairing code expired: asking for a new one")
			continue
		}
		if err := a.SaveToken(token); err != nil {
			return "", fmt.Errorf("saving the pairing: %w", err)
		}
		a.Log.Info("paired")
		a.Paired()
		return token, nil
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

// work claims and prints until ctx ends or the API stops accepting the token.
func (a *Agent) work(ctx context.Context, client API) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go a.heartbeats(ctx, client)

	backoff := minBackoff
	for ctx.Err() == nil {
		jobs, err := client.Claim(ctx, a.ClaimWait)
		if errors.Is(err, api.ErrUnauthorized) {
			return err
		}
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			a.Log.Warn("could not ask for jobs", "err", err)
			if !a.Sleep(ctx, backoff) {
				break
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		backoff = minBackoff
		if unauthorized := a.printAll(ctx, client, jobs); unauthorized {
			return api.ErrUnauthorized
		}
	}
	return ctx.Err()
}

// heartbeats reports the agent alive, with the PC's printers, now and then
// every HeartbeatEvery, until ctx ends.
func (a *Agent) heartbeats(ctx context.Context, client API) {
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
			if err := client.Heartbeat(ctx, printers); err != nil && ctx.Err() == nil {
				a.Log.Warn("heartbeat failed", "err", err)
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
