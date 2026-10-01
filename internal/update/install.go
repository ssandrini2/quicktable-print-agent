package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	// More than any build of the agent weighs; bounds a download.
	maxDownloadBytes = 50 << 20
	downloadTimeout  = 5 * time.Minute
	// How long a new version has to prove it starts.
	checkTimeout = 20 * time.Second
)

// Suffixes of the files an update leaves next to the executable.
const (
	newSuffix = ".new" // the download, until it's swapped in
	oldSuffix = ".old" // the version that was running, kept for going back
)

// Download fetches the release next to exePath (as exePath + ".new") and
// checks it is byte for byte the file the signed manifest names.
func Download(ctx context.Context, release Release, exePath string) (string, error) {
	if err := checkURL(release.URL); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, release.URL, nil)
	if err != nil {
		return "", err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("downloading the update: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading the update: HTTP %d", response.StatusCode)
	}

	path := exePath + newSuffix
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, maxDownloadBytes+1))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	switch {
	case err != nil:
		err = fmt.Errorf("downloading the update: %w", err)
	case written > maxDownloadBytes:
		err = errors.New("the update is larger than an agent can be")
	case hex.EncodeToString(hash.Sum(nil)) != release.SHA256:
		err = errors.New("the downloaded file isn't the one the release names (sha256 mismatch)")
	}
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// checkURL allows https, and plain http only to this same PC (a local API).
func checkURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("malformed update URL: %w", err)
	}
	host := parsed.Hostname()
	local := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if parsed.Scheme == "https" || (parsed.Scheme == "http" && local) {
		return nil
	}
	return errors.New("the update must be downloaded over https")
}

// Check runs the downloaded program just enough to know it starts and is the
// version it should be: "<file> --version" must print exactly that.
func Check(ctx context.Context, path, version string) error {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	// Windows only runs a file as a program when it is named like one.
	runnable := path + ".exe"
	if err := os.Rename(path, runnable); err != nil {
		return err
	}
	out, runErr := exec.CommandContext(ctx, runnable, "--version").Output()
	if err := os.Rename(runnable, path); err != nil {
		return err
	}
	if runErr != nil {
		return fmt.Errorf("the new version doesn't start: %w", runErr)
	}
	if got := strings.TrimSpace(string(out)); got != version {
		return fmt.Errorf("the new version says it is %q, expected %q", got, version)
	}
	return nil
}

// Swap puts the downloaded file in the executable's place, keeping the one
// that is running as exePath + ".old" (Windows lets a running program be
// renamed, not overwritten).
func Swap(exePath, downloaded string) error {
	_ = os.Remove(exePath + oldSuffix)
	if err := os.Rename(exePath, exePath+oldSuffix); err != nil {
		return fmt.Errorf("setting the running version aside: %w", err)
	}
	if err := os.Rename(downloaded, exePath); err != nil {
		// Put it back: the agent must stay startable.
		_ = os.Rename(exePath+oldSuffix, exePath)
		return fmt.Errorf("putting the new version in place: %w", err)
	}
	return nil
}

// Revert undoes Swap: the version kept aside goes back in place. The new
// one must not be running anymore.
func Revert(exePath string) error {
	if _, err := os.Stat(exePath + oldSuffix); err != nil {
		return fmt.Errorf("the previous version is gone: %w", err)
	}
	_ = os.Remove(exePath + newSuffix)
	if err := os.Rename(exePath, exePath+newSuffix); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(exePath+oldSuffix, exePath); err != nil {
		return err
	}
	_ = os.Remove(exePath + newSuffix)
	return nil
}

// Cleanup removes what a finished update left behind.
func Cleanup(exePath string) {
	_ = os.Remove(exePath + oldSuffix)
	_ = os.Remove(exePath + newSuffix)
}

// InWindow reports whether minute (of the day) falls in [start, end); a
// window whose start is after its end wraps past midnight.
func InWindow(minute, start, end int) bool {
	if start == end {
		return false
	}
	if start < end {
		return minute >= start && minute < end
	}
	return minute >= start || minute < end
}
