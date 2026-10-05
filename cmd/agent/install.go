package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/quicktable/print-agent/internal/config"
	"github.com/quicktable/print-agent/internal/i18n"
	"github.com/quicktable/print-agent/internal/platform"
)

// How long a reinstall waits for the running agent to let go of its file.
const replaceWait = 10 * time.Second

// The admin names the download "QuickTable-Impresion-<code>.exe": the code is
// an install approved in advance for one restaurant (64 hex characters; the
// browser may add " (1)" and the like around it).
var installCodeInName = regexp.MustCompile(`[0-9a-f]{64}`)

// install makes the downloaded program the installed one: it asks, copies
// itself to the agent's own folder, sets it to start with Windows, lists it
// among the installed apps and starts that copy. It reports true when this
// process has nothing left to do (it installed, or the person said no);
// false when this already is the installed copy.
func (a *app) install() (done bool, err error) {
	current, err := os.Executable()
	if err != nil {
		return false, err
	}
	target := filepath.Join(a.dir, exeName)
	if samePath(current, target) {
		// Also what refreshes the version Windows shows after an update.
		return false, a.register(target)
	}

	_, statErr := os.Stat(target)
	question := a.texts.InstallPrompt
	if statErr == nil {
		question = a.texts.ReplacePrompt(version)
	}
	if !platform.Confirm(a.texts.Title, question) {
		return true, nil
	}

	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		return false, err
	}
	// The installed copy can't be overwritten while it runs.
	platform.RequestStop()
	if err := copyWithRetry(current, target, replaceWait); err != nil {
		return false, err
	}
	// What the installed copy starts from: the language it was installed in,
	// and the restaurant this download was made for — which replaces whatever
	// pairing an earlier install left.
	lang, hasLang := i18n.Parse(a.opts.lang)
	installCode := installCodeInName.FindString(filepath.Base(current))
	err = a.save(func(c *config.Config) error {
		if hasLang {
			c.Lang = string(lang)
		}
		if installCode != "" {
			c.InstallCode = installCode
			return c.SetPairingToken("")
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	if err := a.register(target); err != nil {
		return false, err
	}
	return true, exec.Command(target).Start()
}

// register sets what Windows knows about the installed agent.
func (a *app) register(exe string) error {
	if err := platform.SetAutostart(exe); err != nil {
		return err
	}
	return platform.RegisterUninstall(exe, a.dir, a.texts.AppName, version)
}

// uninstall removes the agent: it stops the running one, takes it out of
// Windows' startup and installed apps, and deletes its folder (pairing
// included).
func (a *app) uninstall() error {
	if !platform.Confirm(a.texts.Title, a.texts.UninstallPrompt) {
		return nil
	}
	platform.RequestStop()
	if err := platform.RemoveAutostart(); err != nil {
		return err
	}
	if err := platform.RemoveUninstall(); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(a.dir, "config.json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	platform.Notify(a.texts.Title, a.texts.Uninstalled)
	// This very program is in the folder: it goes once this process is gone.
	return platform.RemoveDirLater(a.dir)
}

// copyWithRetry copies from to to, retrying while the target is in use (the
// running agent takes a moment to stop).
func copyWithRetry(from, to string, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		err := copyFile(from, to)
		if err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func copyFile(from, to string) error {
	source, err := os.Open(from)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(to, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	if _, err := io.Copy(target, source); err != nil {
		target.Close()
		return err
	}
	return target.Close()
}

func samePath(a, b string) bool {
	a, errA := filepath.Abs(a)
	b, errB := filepath.Abs(b)
	return errA == nil && errB == nil && strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}
