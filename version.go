// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Ton Kersten

package main

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// version and commit are set when the program is built (see the Makefile and packaging/build.sh):
//
//	go build -ldflags "-X main.version=1.2.3 -X main.commit=0123abcd -X main.repoURL=https://github.com/OWNER/photothing"
//
// The version is a semantic version (https://semver.org) without a leading "v"; it comes from the
// release tag. Without them (for example "go run" or "go install module@vX.Y.Z") the information
// the Go toolchain embeds is used, and as a last resort "0.0.0-dev".
var (
	version = ""
	commit  = ""
)

// repoURL is where the source lives; the About box shows it. Release builds set it to the repository the
// release is built from (see the workflow), so it follows the project when it moves.
var repoURL = "ssh://git@git.smartowl.nl:2990/Development/photothing.git"

// The license of the program, shown in the About box and by --version.
const (
	license    = "GPL-3.0-only"
	licenseURL = "https://www.gnu.org/licenses/gpl-3.0.html"
	// licenseNotice is printed by --version, as the GPL asks of programs that talk to a terminal.
	licenseNotice = "Copyright (C) 2026 Ton Kersten. Licensed under the GNU General Public License version 3.\nThis program comes with ABSOLUTELY NO WARRANTY; see the file LICENSE."
)

// author is shown in the About box. Builds can replace it: -X main.author="Name".
var author = "Ton Kersten"

const devVersion = "0.0.0-dev"

// buildVersion returns the version and the commit of this build.
func buildVersion() (v, c string) {
	v, c = strings.TrimPrefix(version, "v"), commit
	if v != "" && c != "" {
		return v, c
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			v = strings.TrimPrefix(bi.Main.Version, "v")
		}
		if c == "" {
			dirty := false
			for _, s := range bi.Settings {
				switch s.Key {
				case "vcs.revision":
					c = s.Value
				case "vcs.modified":
					dirty = s.Value == "true"
				}
			}
			if c != "" && dirty {
				c += "-dirty"
			}
		}
	}
	if v == "" {
		v = devVersion
	}
	return v, c
}

// versionString is what --version prints.
func versionString() string {
	v, c := buildVersion()
	s := "photothing " + v
	if len(c) > 12 && !strings.HasSuffix(c, "-dirty") {
		c = c[:12]
	} else if strings.HasSuffix(c, "-dirty") && len(c) > 12+len("-dirty") {
		c = c[:12] + "-dirty"
	}
	if c != "" {
		s += " (commit " + c + ")"
	}
	return fmt.Sprintf("%s, %s %s/%s", s, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
