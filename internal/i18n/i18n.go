// Package i18n holds everything the agent says to the person at the PC, in
// Spanish (the default) and English.
package i18n

import (
	"fmt"
	"strings"
)

// Lang is a supported language.
type Lang string

const (
	Spanish Lang = "es"
	English Lang = "en"
)

// Parse reads a language code ("es", "en", "en-US"…). ok is false for
// anything that isn't one of the two.
func Parse(code string) (lang Lang, ok bool) {
	switch strings.ToLower(strings.SplitN(code, "-", 2)[0]) {
	case "es":
		return Spanish, true
	case "en":
		return English, true
	}
	return Spanish, false
}

// Pick chooses the language: what was asked for (a flag), else what was saved
// at install, else English on an English Windows — and Spanish otherwise.
func Pick(flag, saved string, englishWindows bool) Lang {
	for _, code := range []string{flag, saved} {
		if lang, ok := Parse(code); ok {
			return lang
		}
	}
	if englishWindows {
		return English
	}
	return Spanish
}

// Texts is every message, in one language.
type Texts struct {
	// Title is the caption of every window.
	Title string
	// AppName is how Windows lists the agent among the installed apps.
	AppName string

	InstallPrompt       string
	AlreadyRunning      string
	Paired              string
	CodePrompt          string
	WrongCode           string
	OK                  string
	NotNow              string
	UninstallPrompt     string
	Uninstalled         string
	ExitPrompt          string
	DiagnosticsSent     string
	DiagnosticsFailed   string
	DiagnosticsUnpaired string

	// The tray icon's menu.
	StatusConnected string
	StatusOffline   string
	StatusUnpaired  string
	MenuConnect     string
	MenuDiagnostics string
	MenuExit        string

	trayTitle      string
	startFailed    string
	replaceRunning string
}

// TrayTitle names the program and its version.
func (t Texts) TrayTitle(version string) string { return fmt.Sprintf(t.trayTitle, version) }

// StartFailed says the agent couldn't start, and why.
func (t Texts) StartFailed(err error) string { return fmt.Sprintf(t.startFailed, err) }

// ReplacePrompt asks whether to install this version over the one already
// installed and running.
func (t Texts) ReplacePrompt(version string) string { return fmt.Sprintf(t.replaceRunning, version) }

var texts = map[Lang]Texts{
	Spanish: {
		Title:   "QuickTable - Impresión",
		AppName: "QuickTable - Agente de impresión",
		InstallPrompt: "Este programa imprime los tickets de cocina y barra de QuickTable.\n\n" +
			"Se instala solo para tu usuario de Windows y arranca solo al encender la PC. " +
			"No pide contraseña de administrador.\n\n¿Instalarlo en esta PC?",
		AlreadyRunning: "El agente de impresión ya está funcionando en esta PC.",
		Paired: "Listo: esta PC ya imprime los tickets de QuickTable.\n\n" +
			"El programa queda funcionando (lo ves junto al reloj de Windows) y arranca solo con la PC.",
		CodePrompt: "Escribí el código que muestra el administrador de QuickTable\n" +
			"(Impresión > Instalar programa de impresión).",
		WrongCode:           "Ese código no es válido o ya venció.\n\nFijate el que muestra el administrador de QuickTable.",
		OK:                  "Conectar",
		NotNow:              "Ahora no",
		ExitPrompt:          "Si cerrás el programa, los pedidos dejan de imprimirse hasta que lo abras de nuevo o reinicies la PC.\n\n¿Cerrarlo?",
		StatusConnected:     "Conectado",
		StatusOffline:       "Sin conexión a internet",
		StatusUnpaired:      "Sin conectar a un restaurante",
		MenuConnect:         "Conectar a un restaurante",
		MenuDiagnostics:     "Enviar diagnóstico a QuickTable",
		DiagnosticsSent:     "Listo: QuickTable ya recibió el diagnóstico de esta PC.",
		DiagnosticsFailed:   "No se pudo enviar el diagnóstico.\n\nRevisá la conexión a internet y probá de nuevo.",
		DiagnosticsUnpaired: "Esta PC todavía no está conectada a un restaurante.",
		MenuExit:            "Cerrar",
		trayTitle:           "QuickTable Impresión %s",
		UninstallPrompt:     "¿Desinstalar el agente de impresión de QuickTable?\n\nEsta PC deja de imprimir los tickets.",
		Uninstalled: "El agente de impresión se desinstaló.\n\n" +
			"Recordá desvincular esta PC también desde el administrador de QuickTable (Impresión).",
		startFailed:    "El agente de impresión no pudo iniciar:\n\n%v",
		replaceRunning: "El agente de impresión ya está instalado en esta PC.\n\n¿Reemplazarlo por esta versión (%s)?",
	},
	English: {
		Title:   "QuickTable - Printing",
		AppName: "QuickTable - Print agent",
		InstallPrompt: "This program prints QuickTable's kitchen and bar tickets.\n\n" +
			"It installs for your Windows user only and starts by itself when the PC is turned on. " +
			"It doesn't ask for an administrator password.\n\nInstall it on this PC?",
		AlreadyRunning: "The print agent is already running on this PC.",
		Paired: "Done: this PC now prints QuickTable's tickets.\n\n" +
			"The program keeps running (you'll see it next to the Windows clock) and starts with the PC.",
		CodePrompt: "Type the code shown in QuickTable's admin\n" +
			"(Impresión > Instalar programa de impresión).",
		WrongCode:           "That code isn't valid or has expired.\n\nCheck the one QuickTable's admin shows.",
		OK:                  "Connect",
		NotNow:              "Not now",
		ExitPrompt:          "If you close the program, orders stop printing until you open it again or restart the PC.\n\nClose it?",
		StatusConnected:     "Connected",
		StatusOffline:       "No internet connection",
		StatusUnpaired:      "Not connected to a restaurant",
		MenuConnect:         "Connect to a restaurant",
		MenuDiagnostics:     "Send diagnostics to QuickTable",
		DiagnosticsSent:     "Done: QuickTable has this PC's diagnostics.",
		DiagnosticsFailed:   "The diagnostics could not be sent.\n\nCheck the internet connection and try again.",
		DiagnosticsUnpaired: "This PC isn't connected to a restaurant yet.",
		MenuExit:            "Close",
		trayTitle:           "QuickTable Printing %s",
		UninstallPrompt:     "Uninstall QuickTable's print agent?\n\nThis PC will stop printing tickets.",
		Uninstalled: "The print agent was uninstalled.\n\n" +
			"Remember to unpair this PC in QuickTable's admin too (Impresión).",
		startFailed:    "The print agent couldn't start:\n\n%v",
		replaceRunning: "The print agent is already installed on this PC.\n\nReplace it with this version (%s)?",
	},
}

// For returns the texts in lang (Spanish for an unknown one).
func For(lang Lang) Texts {
	if t, ok := texts[lang]; ok {
		return t
	}
	return texts[Spanish]
}
