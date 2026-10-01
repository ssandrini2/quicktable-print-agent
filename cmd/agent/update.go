package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/quicktable/print-agent/internal/api"
	"github.com/quicktable/print-agent/internal/config"
	"github.com/quicktable/print-agent/internal/platform"
	"github.com/quicktable/print-agent/internal/update"
)

const (
	// How long the new version must stay up before the old one leaves.
	probation = 20 * time.Second
	// Downloads that fail (no network) are tried again; after this many in
	// one run the version is given up as failed.
	maxDownloadAttempts = 3
)

// downloadAttempts counts, per version, the downloads that failed in this run.
var downloadAttempts = map[string]int{}

// applyUpdate replaces the running agent with the release:
//
//  1. the release must be signed by a key this build trusts;
//  2. it is downloaded next to the executable and checked against the
//     signed sha256;
//  3. the download must start and say it is that version;
//  4. it takes the executable's place — the running one is kept aside;
//  5. the new version is started and watched: if it is still up after the
//     probation, this process exits; if it dies, the old version is put back
//     and carries on.
//
// It only returns with an error: the agent then keeps working as it is. A
// failure that would repeat is recorded, so that version isn't tried again
// and the API is told.
func (a *app) applyUpdate(ctx context.Context, exe string, release update.Release) error {
	fail := func(err error) error {
		a.recordFailure(release.Version, err)
		return err
	}

	if err := update.Verify(release, update.TrustedKeys()); err != nil {
		return fail(err)
	}
	downloaded, err := update.Download(ctx, release, exe)
	if err != nil {
		downloadAttempts[release.Version]++
		if downloadAttempts[release.Version] >= maxDownloadAttempts {
			return fail(err)
		}
		return err
	}
	if err := update.Check(ctx, downloaded, release.Version); err != nil {
		_ = os.Remove(downloaded)
		return fail(err)
	}
	if err := update.Swap(exe, downloaded); err != nil {
		return fail(err)
	}

	// From here on the new version is in place. Step aside and let it run.
	a.releaseInstance()
	a.log.Info("starting the new version", "version", release.Version)
	startErr := runOnProbation(exe)
	if startErr == nil {
		a.log.Info("updated: handing over", "version", release.Version)
		os.Exit(0)
	}

	// It didn't hold: back to the version that was running.
	a.log.Error("the new version didn't stay up: going back", "version", release.Version, "err", startErr)
	if err := update.Revert(exe); err != nil {
		a.log.Error("could not put the previous version back", "err", err)
	}
	if only, release, err := platform.SingleInstance(); err == nil && only {
		a.releaseInstance = release
	}
	return fail(fmt.Errorf("the new version didn't stay up: %w", startErr))
}

// runOnProbation starts the new version and waits to see it stay up. An error
// means it couldn't start or stopped within the probation.
func runOnProbation(exe string) error {
	child := exec.Command(exe, "--after-update")
	if err := child.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()
	select {
	case err := <-exited:
		if err == nil {
			err = errors.New("it stopped right after starting")
		}
		return err
	case <-time.After(probation):
		return nil
	}
}

// recordFailure remembers an update that didn't work.
func (a *app) recordFailure(failedVersion string, cause error) {
	err := a.save(func(c *config.Config) error {
		c.FailedUpdate = &config.FailedUpdate{Version: failedVersion, Error: cause.Error(), From: version}
		return nil
	})
	if err != nil {
		a.log.Error("could not record the failed update", "err", err)
	}
}

// failedUpdate is the update this version tried and went back from, if any.
func (a *app) failedUpdate() *api.UpdateFailure {
	a.mu.Lock()
	defer a.mu.Unlock()
	failed := a.saved.FailedUpdate
	if failed == nil {
		return nil
	}
	return &api.UpdateFailure{Version: failed.Version, Error: failed.Error}
}

// forgetStaleFailure drops a failure recorded by another version: this one
// got here some other way, so the record no longer says anything.
func (a *app) forgetStaleFailure() {
	a.mu.Lock()
	stale := a.saved.FailedUpdate != nil && a.saved.FailedUpdate.From != version
	a.mu.Unlock()
	if !stale {
		return
	}
	if err := a.save(func(c *config.Config) error { c.FailedUpdate = nil; return nil }); err != nil {
		a.log.Warn("could not clear the old update failure", "err", err)
	}
}
