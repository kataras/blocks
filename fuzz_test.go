package blocks

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The text transformations below run on every file before it is parsed, and they are
// regular expressions over input the package does not control. A panic in any of them
// takes down Load, so the property each target checks is mostly "survives anything", plus
// the one invariant that transformation actually promises.

func FuzzRemoveComments(f *testing.F) {
	f.Add("<p>plain</p>")
	f.Add("<!-- comment -->")
	f.Add("<!-- multi\nline\ncomment --><p>after</p>")
	f.Add("<!<!---->-->")
	f.Add("<!--")
	f.Add("-->")
	f.Add("<!-- {{ define \"content\" }} -->")

	f.Fuzz(func(t *testing.T, in string) {
		out := removeComments(in)

		// Comments are only ever removed, so the result cannot grow. It is not
		// idempotent: stripping the inner comment of "<!<!---->-->" leaves a new one, and
		// a second pass would remove that too.
		if len(out) > len(in) {
			t.Errorf("output grew from %d to %d bytes", len(in), len(out))
		}
	})
}

func FuzzReplaceYield(f *testing.F) {
	f.Add("{{ yield . }}", "{{", "}}")
	f.Add("{{ yield }}", "{{", "}}")
	f.Add("{{- yield . -}}", "{{", "}}")
	f.Add("[[ yield . ]]", "[[", "]]")
	f.Add("{{ yield {{ yield }} }}", "{{", "}}")
	f.Add("no actions here", "{{", "}}")
	f.Add("{{ yielding }}", "{{", "}}")

	f.Fuzz(func(t *testing.T, in, left, right string) {
		if left == "" || right == "" || strings.Contains(left, right) || strings.Contains(right, left) {
			t.Skip("overlapping delimiters are not a configuration this package accepts")
		}
		if !utf8.ValidString(left) || !utf8.ValidString(right) {
			t.Skip("Delims rejects a delimiter that is not valid UTF-8")
		}

		pat := newPatterns(left, right)
		out := pat.replaceYield(in)

		// A file with no yield is passed through untouched. Anything else would rewrite
		// templates that never asked for it.
		if !pat.yield.MatchString(in) && out != in {
			t.Errorf("input without a yield was changed:\nin:  %q\nout: %q", in, out)
		}
	})
}

func FuzzWrapContent(f *testing.F) {
	f.Add("<p>plain</p>", "{{", "}}")
	f.Add("{{ define \"content\" }}x{{ end }}", "{{", "}}")
	f.Add("{{- define \"content\" }}x{{ end }}", "{{", "}}")
	f.Add("{{define \"title\"}}x{{end}}", "{{", "}}")
	f.Add("[[ define \"content\" ]]x[[ end ]]", "[[", "]]")

	f.Fuzz(func(t *testing.T, in, left, right string) {
		if left == "" || right == "" || strings.Contains(left, right) || strings.Contains(right, left) {
			t.Skip("overlapping delimiters are not a configuration this package accepts")
		}
		if !utf8.ValidString(left) || !utf8.ValidString(right) {
			t.Skip("Delims rejects a delimiter that is not valid UTF-8")
		}

		pat := newPatterns(left, right)
		out := pat.wrapContent(in)

		// Wrapping only adds a define around the file, so the original text survives, and
		// a file that defines something is left alone.
		if !strings.Contains(out, in) {
			t.Errorf("the original text was lost:\nin:  %q\nout: %q", in, out)
		}
		if pat.define.MatchString(in) && out != in {
			t.Errorf("a file that already defines a template was wrapped:\nin:  %q\nout: %q", in, out)
		}
	})
}

// FuzzLayoutDetection covers the decision that sends a file down one path or the other. A
// file with no action at all is never a layout.
func FuzzLayoutDetection(f *testing.F) {
	f.Add("{{ template \"content\" . }}")
	f.Add("{{ yield . }}")
	f.Add("{{ yielding }}")
	f.Add("<p>just a page</p>")
	f.Add("{{ template \"other\" . }}")

	f.Fuzz(func(t *testing.T, in string) {
		pat := newPatterns("{{", "}}")

		if pat.layout.MatchString(in) && !strings.Contains(in, "{{") {
			t.Errorf("a file with no action was read as a layout: %q", in)
		}
	})
}
