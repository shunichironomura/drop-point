package dropname

import (
	"errors"
	"regexp"

	petname "github.com/dustinkirkland/golang-petname"
)

const (
	words     = 2
	separator = "-"
)

var displayNamePattern = regexp.MustCompile(`^[a-z]+-[a-z]+$`)

// Generate returns a human-readable, non-secret display name for a drop point.
func Generate() (string, error) {
	for range 3 {
		name := petname.Generate(words, separator)
		if Valid(name) {
			return name, nil
		}
	}
	return "", errors.New("generate drop point display name")
}

// Valid reports whether name has the supported adjective-noun shape.
func Valid(name string) bool {
	return displayNamePattern.MatchString(name)
}
