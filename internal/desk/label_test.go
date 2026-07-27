package desk

import "testing"

func TestLabel(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"firefox", "firefox"},
		{"Alacritty", "alacritty"},
		{"org.mozilla.firefox", "firefox"},
		{"com.mitchellh.ghostty", "ghostty"},
		{"Google-chrome", "google-chrome"},
		{"code-oss", "code-oss"},
		{"LibreOffice Writer", "libreoffice-writer"},
		{"foot_client", "foot-client"},
	} {
		got, ok := Label(c.in)
		if !ok || got != c.want {
			t.Errorf("Label(%q) = %q, %v, want %q", c.in, got, ok, c.want)
		}
	}
}

// An app id that leaves nothing a name can hold falls back to an ordinal
// rather than producing a label nobody can read or niri will not take.
func TestLabelRefusesWhatIsNotAName(t *testing.T) {
	for _, in := range []string{"", "...", "---", "ф", "12", "org.example.7"} {
		if got, ok := Label(in); ok {
			t.Errorf("Label(%q) = %q, want a refusal", in, got)
		}
	}
	// Whatever it does return has to be a legal slot.
	for _, in := range []string{"Firefox", "a b c", "X.Y.Z-1"} {
		if got, ok := Label(in); ok {
			if _, err := NewName("vshop", "DP-1", got); err != nil {
				t.Errorf("Label(%q) = %q, which is not a slot: %v", in, got, err)
			}
		}
	}
}
