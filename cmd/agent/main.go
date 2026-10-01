// The QuickTable print agent: a background app on the restaurant's cash PC
// that prints the kitchen and bar tickets the API queues.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/quicktable/print-agent/internal/agent"
	"github.com/quicktable/print-agent/internal/api"
	"github.com/quicktable/print-agent/internal/config"
	"github.com/quicktable/print-agent/internal/platform"
	"github.com/quicktable/print-agent/internal/transport"
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
)

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	uninstall := flag.Bool("uninstall", false, "stop starting with Windows and forget the pairing")
	noInstall := flag.Bool("no-install", false, "run from where it is instead of installing itself (development)")
	console := flag.Bool("console", false, "also log to the console")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	if err := run(*uninstall, *noInstall, *console); err != nil {
		platform.Notify("El agente de impresión no pudo iniciar:\n\n" + err.Error())
		os.Exit(1)
	}
}

func run(uninstall, noInstall, console bool) error {
	dir, err := config.DefaultDir()
	if err != nil {
		return err
	}
	store := config.Store{Dir: dir}

	if uninstall {
		return doUninstall(store)
	}
	if !noInstall && runtime.GOOS == "windows" {
		if moved, err := installSelf(dir); err != nil || moved {
			return err
		}
	}

	only, err := platform.SingleInstance()
	if err != nil {
		return err
	}
	if !only {
		platform.Notify("El agente de impresión ya está funcionando en esta PC.")
		return nil
	}

	log, closeLog, err := openLog(dir, console)
	if err != nil {
		return err
	}
	defer closeLog()

	saved, err := store.Load()
	if err != nil {
		// A damaged file: start over, unpaired.
		log.Warn("the saved configuration could not be read; starting unpaired", "err", err)
		saved = config.Config{}
	}
	apiURL := firstNonEmpty(os.Getenv(apiURLEnvVar), saved.APIURL, defaultAPIURL)
	log.Info("print agent starting", "version", version, "api", apiURL)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// One pairing notice on screen at a time: closed once the person closes it.
	noticeClosed := make(chan struct{})
	close(noticeClosed)
	worker := &agent.Agent{
		Connect:   func(token string) agent.API { return api.New(apiURL, token, version) },
		LoadToken: func() string { return saved.PairingToken() },
		SaveToken: func(token string) error {
			if err := saved.SetPairingToken(token); err != nil {
				return err
			}
			return store.Save(saved)
		},
		Send:              transport.Send,
		InstalledPrinters: transport.InstalledPrinters,
		BeforeCode: func(ctx context.Context) bool {
			select {
			case <-noticeClosed:
				return true
			case <-ctx.Done():
				return false
			}
		},
		ShowCode: func(userCode string) {
			closed := make(chan struct{})
			noticeClosed = closed
			go func() {
				defer close(closed)
				platform.Notify(pairingText(userCode))
			}()
		},
		Paired: func() {
			go platform.Notify("Listo: esta PC ya imprime los tickets de QuickTable.\n\nEl agente queda funcionando en segundo plano y arranca solo con Windows.")
		},
		Log: log,
	}
	err = worker.Run(ctx)
	log.Info("print agent stopped", "reason", err)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// pairingText is what the person at the PC reads to pair the agent.
func pairingText(userCode string) string {
	code := userCode
	if len(code) == 8 {
		code = code[:4] + " " + code[4:]
	}
	return "Código de vinculación:\n\n        " + code + "\n\n" +
		"En el administrador de QuickTable entrá a Impresión > Vincular PC e ingresá este código.\n" +
		"El código vence en unos minutos; al cerrar este aviso se muestra uno nuevo si todavía hace falta."
}

// installSelf copies the running program to the agent's own folder, sets it
// to start with Windows and starts that copy. It reports true when it did, so
// the caller exits; false when this already is the installed copy.
func installSelf(dir string) (bool, error) {
	current, err := os.Executable()
	if err != nil {
		return false, err
	}
	target := filepath.Join(dir, exeName)
	if samePath(current, target) {
		return false, platform.SetAutostart(target)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	if err := copyFile(current, target); err != nil {
		// The installed copy is in use: the agent is already running.
		platform.Notify("El agente de impresión ya está instalado y funcionando en esta PC.\n\nPara reinstalarlo, cerralo primero desde el Administrador de tareas.")
		return true, nil
	}
	if err := platform.SetAutostart(target); err != nil {
		return false, err
	}
	return true, exec.Command(target).Start()
}

func doUninstall(store config.Store) error {
	if err := platform.RemoveAutostart(); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(store.Dir, "config.json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	platform.Notify("El agente de impresión ya no arranca con Windows y olvidó su vinculación.\n\nRecordá desvincular esta PC también desde el administrador de QuickTable.")
	return nil
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
