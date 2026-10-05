package i18n

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestPick(t *testing.T) {
	cases := []struct {
		flag, saved    string
		englishWindows bool
		want           Lang
	}{
		{"", "", false, Spanish},
		{"", "", true, English},
		{"", "es", true, Spanish},
		{"en", "es", false, English},
		{"en-US", "", false, English},
		{"fr", "", false, Spanish},
		{"fr", "en", false, English},
	}
	for _, c := range cases {
		if got := Pick(c.flag, c.saved, c.englishWindows); got != c.want {
			t.Errorf("Pick(%q, %q, %v) = %s, want %s", c.flag, c.saved, c.englishWindows, got, c.want)
		}
	}
}

func TestEveryTextExistsInBothLanguages(t *testing.T) {
	for _, lang := range []Lang{Spanish, English} {
		value := reflect.ValueOf(For(lang))
		for i := 0; i < value.NumField(); i++ {
			if value.Field(i).String() == "" {
				t.Errorf("%s: %s is empty", lang, value.Type().Field(i).Name)
			}
		}
	}
}

func TestMessagesWithValues(t *testing.T) {
	spanish := For(Spanish)
	if got := spanish.StartFailed(errors.New("disk full")); !strings.Contains(got, "disk full") {
		t.Errorf("got %q", got)
	}
	if got := For(English).ReplacePrompt("1.2.0"); !strings.Contains(got, "1.2.0") {
		t.Errorf("got %q", got)
	}
	if For("fr").Title != spanish.Title {
		t.Error("an unknown language falls back to Spanish")
	}
}
