// The QuickTable print agent: a background app on the restaurant's cash PC
// that prints the kitchen and bar tickets the API queues. The same executable
// is its own installer, uninstaller and updater.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quicktable/print-agent/internal/agent"
	"github.com/quicktable/print-agent/internal/api"
	"github.com/quicktable/print-agent/internal/config"
	"github.com/quicktable/print-agent/internal/i18n"
	"github.com/quicktable/print-agent/internal/platform"
	"github.com/quicktable/print-agent/internal/transport"
	"github.com/quicktable/print-agent/internal/tray"
	"github.com/quicktable/print-agent/internal/update"
)

// Set at build time: -ldflags "-X main.version=1.2.3 -X main.defaultAPIURL=https://…".
var (
	version       = "dev"
	defaultAPIURL = "http://localhost:3000"
)

const (
	exeName      = "quicktable-print-agent.exe"
	logName      = "agent.log"
	maxLogBytes  = 5 << 20
	apiURLEnvVar = "QT_PRINT_AGENT_API_URL"
	// How long the new version waits for the one it replaces to step aside.
	handoverWait = 15 * time.Second
	// What an update left behind is removed once the new version has run this long.
	cleanupAfter = 2 * time.Minute
)

type options struct {
	uninstall   bool
	noInstall   bool
	console     bool
	afterUpdate bool
	lang        string
}

func main() {
	var opts options
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.BoolVar(&opts.uninstall, "uninstall", false, "remove the agent from this PC")
	flag.BoolVar(&opts.noInstall, "no-install", false, "run from where it is: no install, no updates (development)")
	flag.BoolVar(&opts.console, "console", false, "also log to the console")
	flag.BoolVar(&opts.afterUpdate, "after-update", false, "internal: started by the version this one replaces")
	flag.StringVar(&opts.lang, "lang", "", "language of the messages: es or en (default: Spanish, English on an English Windows)")
	flag.Parse()

	// Nothing else may happen here: an update runs "--version" on the new
	// file to check it starts (see update.Check).
	if *showVersion {
		fmt.Println(version)
		return
	}

	dir, err := config.DefaultDir()
	if err != nil {
		platform.Notify(i18n.For(i18n.Spanish).Title, i18n.For(i18n.Spanish).StartFailed(err))
		os.Exit(1)
	}
	store := config.Store{Dir: dir}
	// A damaged file means starting over, unpaired.
	saved, loadErr := store.Load()
	if loadErr != nil {
		saved = config.Config{}
	}
	texts := i18n.For(i18n.Pick(opts.lang, saved.Lang, platform.EnglishUI()))

	app := &app{opts: opts, dir: dir, store: store, saved: saved, texts: texts}
	if err := app.run(loadErr); err != nil {
		platform.Notify(texts.Title, texts.StartFailed(err))
		os.Exit(1)
	}
}

// app is one run of the program.
type app struct {
	opts  options
	dir   string
	store config.Store
	texts i18n.Texts
	log   *slog.Logger

	// saved is shared by the work loop and the heartbeat.
	mu    sync.Mutex
	saved config.Config

	// releaseInstance gives up being "the" running agent (see applyUpdate).
	releaseInstance func()
}

func (a *app) run(loadErr error) error {
	if a.opts.uninstall {
		return a.uninstall()
	}
	installs := !a.opts.noInstall && runtime.GOOS == "windows"
	if installs {
		if done, err := a.install(); err != nil || done {
			return err
		}
	}

	only, err := a.claimInstance()
	if err != nil {
		return err
	}
	if !only {
		platform.Notify(a.texts.Title, a.texts.AlreadyRunning)
		return nil
	}

	log, closeLog, err := openLog(a.dir, a.opts.console)
	if err != nil {
		return err
	}
	defer closeLog()
	a.log = log
	if loadErr != nil {
		log.Warn("the saved configuration could not be read; starting unpaired", "err", loadErr)
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if a.opts.afterUpdate {
		// The version this one replaced may still need its copy to come back.
		time.AfterFunc(cleanupAfter, func() { update.Cleanup(exe) })
	} else if installs {
		update.Cleanup(exe)
	}
	a.forgetStaleFailure()

	apiURL := firstNonEmpty(os.Getenv(apiURLEnvVar), a.saved.APIURL, defaultAPIURL)
	log.Info("print agent starting", "version", version, "api", apiURL, "afterUpdate", a.opts.afterUpdate)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	// An uninstall, or a reinstall over this one, asks it to stop.
	if stopRequested, err := platform.StopRequested(); err != nil {
		log.Warn("can't listen for a stop request", "err", err)
	} else {
		go func() {
			select {
			case <-stopRequested:
				log.Info("asked to stop")
				stop()
			case <-ctx.Done():
			}
		}()
	}

	// The person asking, from the icon's menu, to connect with a code.
	codeRequests := make(chan struct{}, 1)
	var shown atomic.Int32
	shown.Store(-1)
	var workErr error
	tray.Run(tray.Options{
		Title:        a.texts.TrayTitle(version),
		ConnectLabel: a.texts.MenuConnect,
		OnConnect: func() {
			select {
			case codeRequests <- struct{}{}:
			default:
			}
		},
		ExitLabel: a.texts.MenuExit,
		OnExit: func() {
			if platform.Confirm(a.texts.Title, a.texts.ExitPrompt) {
				log.Info("closed from the tray icon")
				stop()
			}
		},
	}, func(icon *tray.Tray) {
		worker := &agent.Agent{
			Connect:   func(token string) agent.API { return api.New(apiURL, token, version) },
			LoadToken: func() string { a.mu.Lock(); defer a.mu.Unlock(); return a.saved.PairingToken() },
			SaveToken: func(token string) error {
				return a.save(func(c *config.Config) error { return c.SetPairingToken(token) })
			},
			InstallCode: func() string { a.mu.Lock(); defer a.mu.Unlock(); return a.saved.InstallCode },
			ClearInstallCode: func() error {
				return a.save(func(c *config.Config) error { c.InstallCode = ""; return nil })
			},
			Send:              transport.Send,
			InstalledPrinters: transport.InstalledPrinters,
			Unpaired:          func() { go platform.Notify(a.texts.Title, a.texts.NotConnected) },
			CodeRequested: func(ctx context.Context) bool {
				select {
				case <-codeRequests:
					return true
				case <-ctx.Done():
					return false
				}
			},
			ShowCode: func(userCode string) { go platform.Notify(a.texts.Title, a.texts.PairingCode(userCode)) },
			Paired:   func() { go platform.Notify(a.texts.Title, a.texts.Paired) },
			Status: func(state agent.State) {
				// Told on every request that works: only a change touches the icon.
				if shown.Swap(int32(state)) == int32(state) {
					return
				}
				switch state {
				case agent.Connected:
					icon.SetStatus(a.texts.StatusConnected)
				case agent.Offline:
					icon.SetStatus(a.texts.StatusOffline)
				default:
					icon.SetStatus(a.texts.StatusUnpaired)
				}
				icon.OfferConnect(state == agent.Unpaired)
			},
			FailedUpdate: a.failedUpdate,
			Log:          log,
		}
		if installs {
			worker.ApplyUpdate = func(ctx context.Context, release update.Release) error {
				return a.applyUpdate(ctx, exe, release)
			}
		}
		workErr = worker.Run(ctx)
	})
	log.Info("print agent stopped", "reason", workErr)
	if ctx.Err() != nil {
		return nil
	}
	return workErr
}

// claimInstance makes this the one running agent. Right after an update the
// version being replaced may take a moment to step aside.
func (a *app) claimInstance() (bool, error) {
	deadline := time.Now().Add(handoverWait)
	for {
		only, release, err := platform.SingleInstance()
		if err != nil {
			return false, err
		}
		if only {
			a.releaseInstance = release
			return true, nil
		}
		if !a.opts.afterUpdate || time.Now().After(deadline) {
			return false, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// save changes the saved configuration and writes it.
func (a *app) save(change func(*config.Config) error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := change(&a.saved); err != nil {
		return err
	}
	return a.store.Save(a.saved)
}

// openLog logs to agent.log in dir, starting a new file once it gets big.
func openLog(dir string, console bool) (*slog.Logger, func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	path := filepath.Join(dir, logName)
	if info, err := os.Stat(path); err == nil && info.Size() > maxLogBytes {
		_ = os.Rename(path, path+".1")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, err
	}
	var out io.Writer = file
	if console {
		out = io.MultiWriter(file, os.Stderr)
	}
	return slog.New(slog.NewTextHandler(out, nil)), func() { _ = file.Close() }, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
