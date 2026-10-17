package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// EditOutcome says whether SetImage changed the file. Reason is set when
// it did not, naming why the file was deliberately left alone: one of the
// Skip constants, or a description of an image declaration it could not
// safely rewrite. None of these is an error -- the caller prints the line
// for the user to add instead.
type EditOutcome struct {
	Written bool
	Reason  string
}

// The reasons a file is left alone before its contents are read. They are
// named because callers explain them in their own words, and they appear
// verbatim in image build's --json output, so their values are fixed.
const (
	SkipSymlink  = "symlink"
	SkipNixStore = "nix store"
	SkipReadOnly = "read-only"
)

// nixStorePrefix is a variable so tests can stand a temp dir in for the
// store, which they cannot write to.
var nixStorePrefix = "/nix/store/"

// imageDecl counts any `image:` line as a declaration, typed or not, so
// an annotation imageLiteral cannot follow is refused rather than
// answered with a second, conflicting image line. imageLiteral accepts
// only a plain type name between the colon and the equals sign.
var (
	imageDecl    = regexp.MustCompile(`^[ \t]*image[ \t]*[:=]`)
	imageLiteral = regexp.MustCompile(`^([ \t]*image[ \t]*(?::[ \t]*[A-Za-z_][A-Za-z0-9_.]*\??[ \t]*)?=[ \t]*)"(?:[^"\\]|\\.)*"([ \t]*(?://.*)?)$`)
)

// SetImage points path's top-level image at ref by editing that one line,
// or appending one when the file has none.
//
// Render cannot do this: it writes a whole file from sparse Values, and
// a hand-written base.pkl holds comments, expressions and amends that a
// re-render would throw away. So this touches only the bytes of the
// image declaration and refuses anything it cannot rewrite exactly --
// an image computed by an expression, or a file that is not the user's
// to change. A Home Manager base.pkl is a symlink into the Nix store,
// and replacing the link with a regular file would silently detach it
// from the user's Home Manager configuration until the next switch
// clobbered the edit.
//
// The write goes through a temp file and rename in the same directory,
// so a crash leaves the old file or the new one, never half of each.
func SetImage(path, ref string) (EditOutcome, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return EditOutcome{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return EditOutcome{Reason: SkipSymlink}, nil
	}
	if inNixStore(path) {
		return EditOutcome{Reason: SkipNixStore}, nil
	}
	if info.Mode().Perm()&0o200 == 0 {
		return EditOutcome{Reason: SkipReadOnly}, nil
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- the config the user selected
	if err != nil {
		return EditOutcome{}, err
	}
	updated, reason := withImage(string(raw), ref)
	if reason != "" {
		return EditOutcome{Reason: reason}, nil
	}
	if err := replaceFile(path, []byte(updated), info.Mode().Perm()); err != nil {
		return EditOutcome{}, err
	}
	return EditOutcome{Written: true}, nil
}

func inNixStore(path string) bool {
	paths := []string{path}
	if abs, err := filepath.Abs(path); err == nil {
		paths = append(paths, abs)
	}
	// A regular file reached through a symlinked parent directory is as
	// much the store's as one linked directly.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		paths = append(paths, real)
	}
	for _, p := range paths {
		if strings.HasPrefix(p, nixStorePrefix) {
			return true
		}
	}
	return false
}

// withImage returns src with its top-level image set to ref, or a reason
// it could not be done safely.
func withImage(src, ref string) (string, string) {
	literal := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(ref) + `"`
	var found []int
	for _, start := range topLevelLineStarts(src) {
		if imageDecl.MatchString(src[start:lineEnd(src, start)]) {
			found = append(found, start)
		}
	}
	switch len(found) {
	case 0:
		if src != "" && !strings.HasSuffix(src, "\n") {
			src += "\n"
		}
		return src + "image = " + literal + "\n", ""
	case 1:
	default:
		return "", "image is declared more than once"
	}
	start := found[0]
	end := lineEnd(src, start)
	m := imageLiteral.FindStringSubmatch(src[start:end])
	if m == nil {
		return "", "image is not a plain string literal"
	}
	return src[:start] + m[1] + literal + m[2] + src[end:], ""
}

func lineEnd(src string, start int) int {
	if i := strings.IndexByte(src[start:], '\n'); i >= 0 {
		return start + i
	}
	return len(src)
}

// topLevelLineStarts returns the offset of every line that begins outside
// any bracket, comment or string: the only places a declaration of the
// config's own image can start. A line-by-line regex alone would match
// `image =` inside a nested object, a block comment or a multi-line
// string, and rewrite a value that only looks like the one asked for.
//
// It understands as much Pkl lexing as that needs -- line and block
// comments, single- and multi-line strings with custom # delimiters --
// and no more; brackets of all three kinds count as nesting.
func topLevelLineStarts(src string) []int {
	const (
		normal = iota
		lineComment
		blockComment
		str
		multiStr
	)
	state, depth, hashes := normal, 0, 0
	closes := func(i int, quotes string) bool {
		return strings.HasPrefix(src[i:], quotes+strings.Repeat("#", hashes))
	}
	var starts []int
	for i := 0; i < len(src); i++ {
		if (i == 0 || src[i-1] == '\n') && state == normal && depth == 0 {
			starts = append(starts, i)
		}
		c := src[i]
		switch state {
		case lineComment:
			if c == '\n' {
				state = normal
			}
		case blockComment:
			if strings.HasPrefix(src[i:], "*/") {
				state, i = normal, i+1
			}
		case str, multiStr:
			quotes := `"`
			if state == multiStr {
				quotes = `"""`
			}
			switch {
			case c == '\\' && strings.HasPrefix(src[i+1:], strings.Repeat("#", hashes)):
				i += hashes + 1 // the escaped character is never a delimiter
			case closes(i, quotes):
				state, i = normal, i+len(quotes)+hashes-1
			case c == '\n' && state == str:
				state = normal // unterminated; recover at the line end
			}
		default:
			switch {
			case strings.HasPrefix(src[i:], "//"):
				state = lineComment
			case strings.HasPrefix(src[i:], "/*"):
				state, i = blockComment, i+1
			case c == '#' || c == '"':
				n := 0
				for i+n < len(src) && src[i+n] == '#' {
					n++
				}
				if i+n >= len(src) || src[i+n] != '"' {
					break
				}
				hashes = n
				if strings.HasPrefix(src[i+n:], `"""`) {
					state, i = multiStr, i+n+2
				} else {
					state, i = str, i+n
				}
			case c == '{' || c == '(' || c == '[':
				depth++
			case (c == '}' || c == ')' || c == ']') && depth > 0:
				depth--
			}
		}
	}
	return starts
}

func replaceFile(path string, data []byte, mode os.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	_, werr := tmp.Write(data)
	err = errors.Join(werr, tmp.Chmod(mode), tmp.Sync(), tmp.Close())
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
