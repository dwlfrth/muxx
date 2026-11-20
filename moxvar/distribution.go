package moxvar

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
)

// Distribution is set at build time with -ldflags -X for release builds of a
// distribution of mox, to the name of the release tag, e.g. "muxx-202611191730":
// the product name, a dash, and the release. Builds without it are plain mox.
var Distribution string

// distributionParts returns the product name and release from Distribution, or
// "mox" and "" for a plain mox build.
func distributionParts() (product, release string) {
	return parseDistribution(Distribution)
}

// distributionRegexp matches valid values for Distribution: a product name in lower
// case, and optionally a dash and the release. The name ends up in header names,
// file names and environment variables, so it is kept to letters and digits.
var distributionRegexp = regexp.MustCompile(`^[a-z][a-z0-9]*(-[A-Za-z0-9.]+)?$`)

// checkDistribution returns an error if dist is set but not valid.
func checkDistribution(dist string) error {
	if dist != "" && !distributionRegexp.MatchString(dist) {
		return fmt.Errorf("invalid distribution %q set at build time, must be a lower case name of letters and digits, optionally followed by a dash and release of letters, digits and dots, e.g. \"muxx-202611191730\"", dist)
	}
	return nil
}

// parseDistribution returns the product name and release of distribution dist,
// e.g. "muxx" and "202611191730" for "muxx-202611191730". A value without product
// name, like "" or "-x", is plain mox: "mox" and "".
func parseDistribution(dist string) (product, release string) {
	product, release, _ = strings.Cut(dist, "-")
	if product == "" {
		return "mox", ""
	}
	return product, release
}

// title returns s with its first letter in upper case. s must not be empty.
func title(s string) string {
	return strings.ToUpper(s[:1]) + s[1:]
}

// ProductName returns the name the program presents itself with, "mox" unless
// built as a distribution.
func ProductName() string {
	product, _ := distributionParts()
	return product
}

// ProductTitle returns ProductName with its first letter in upper case, e.g.
// "Mox", for use at the start of titles and header names.
func ProductTitle() string {
	return title(ProductName())
}

// HeaderPrefix returns the prefix of the message and HTTP headers the program adds
// and recognizes, "X-Mox-" unless built as a distribution.
func HeaderPrefix() string {
	return "X-" + ProductTitle() + "-"
}

// ConfigFileName returns the default name of the static configuration file,
// "mox.conf" unless built as a distribution.
func ConfigFileName() string {
	return ProductName() + ".conf"
}

// EnvName returns the name of an environment variable documented for users, e.g.
// EnvName("CONF") is "MOXCONF" and EnvName("_DOCKER") is "MOX_DOCKER" unless built
// as a distribution.
func EnvName(suffix string) string {
	return strings.ToUpper(ProductName()) + suffix
}

// UserAgent returns the value for User-Agent headers, "mox/<version>" unless built
// as a distribution, e.g. "muxx/202611191730 (mox v0.0.18-...)".
func UserAgent() string {
	product, release := distributionParts()
	if product == "mox" {
		return "mox/" + Version
	}
	return product + "/" + release + " (mox " + baseVersion + ")"
}

// withDistribution returns the version to show for a build with distribution
// dist, based on the mox version base. Without distribution, base is returned.
// The web interfaces derive the product name from this format, by the " (mox "
// in it, see productName in lib.ts.
func withDistribution(dist, base string) string {
	product, release := parseDistribution(dist)
	if product == "mox" {
		return base
	}
	if release == "" {
		return product + " (mox " + base + ")"
	}
	return product + " " + release + " (mox " + base + ")"
}

// Matches the word mox in its usual spellings, and the documented environment
// variables, for Rename.
var renameRegexp = regexp.MustCompile(`\b(mox|Mox|MOX|MOXCONF|MOX_DOCKER)\b`)

// Rename returns text meant for users, such as help texts and log messages, with
// the name mox replaced by the product name, for builds as a distribution. Without
// distribution, the text is returned unchanged.
//
// Kept unchanged are references to mox itself: the module path and links to it
// (github.com/mjl-/mox), example domains (mox.example) and addresses at mox
// (mox@localhost, the account of localserve). Text that only contains mox as part
// of a longer word, like xmox.nl, is not matched.
//
// Only for text as written in the code, e.g. a format string before formatting,
// never for text containing data: paths, hostnames or passwords with the word mox
// would be changed too.
func Rename(s string) string {
	product := ProductName()
	if product == "mox" {
		return s
	}
	return renameText(s, product)
}

func renameText(s, product string) string {
	productTitle := title(product)
	upper := strings.ToUpper(product)
	var b strings.Builder
	last := 0
	for _, m := range renameRegexp.FindAllStringIndex(s, -1) {
		start, end := m[0], m[1]
		before, after := s[:start], s[end:]
		if strings.HasSuffix(before, "mjl-/") || strings.HasPrefix(after, ".example") || strings.HasPrefix(after, "@") || strings.HasPrefix(after, "-/") {
			continue
		}
		var repl string
		switch word := s[start:end]; word {
		case "mox":
			repl = product
		case "Mox":
			repl = productTitle
		case "MOX":
			repl = upper
		case "MOXCONF":
			repl = upper + "CONF"
		case "MOX_DOCKER":
			repl = upper + "_DOCKER"
		}
		b.WriteString(s[last:start])
		b.WriteString(repl)
		last = end
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// RenameComments returns a configuration file in sconf format with Rename applied
// to its comment lines only, as its values are data.
func RenameComments(conf string) string {
	lines := strings.SplitAfter(conf, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "#") {
			lines[i] = Rename(line)
		}
	}
	return strings.Join(lines, "")
}

// RenameTitle returns an HTML page with the product title in place of "Mox" at the
// start of its <title>, e.g. "Muxx Webmail", for builds as a distribution. Pages
// show this title before their JavaScript sets one, e.g. on the login screen.
func RenameTitle(html []byte) []byte {
	product := ProductTitle()
	if product == "Mox" {
		return html
	}
	return bytes.Replace(html, []byte("<title>Mox "), []byte("<title>"+product+" "), 1)
}
