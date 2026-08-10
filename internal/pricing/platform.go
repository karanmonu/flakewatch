package pricing

import "strings"

// Platform groups runner labels into the OS families that differ in price.
type Platform string

const (
	Linux           Platform = "linux"
	MacOS           Platform = "macos"
	Windows         Platform = "windows"
	UnknownPlatform Platform = ""
)

// LinuxEquivalentUSDPerMinute prices the counterfactual "what if this job ran
// on a standard Linux runner instead".
//
// Deliberately the standard rate, not the slim one. Comparing against the
// cheapest SKU that exists would overstate the difference by tripling it, on
// top of a change the user has not agreed to make.
const LinuxEquivalentUSDPerMinute = UbuntuUSDPerMinute

// PlatformOf classifies a runner label.
func PlatformOf(label string) Platform {
	l := strings.ToLower(label)
	switch {
	case strings.HasPrefix(l, "macos"):
		return MacOS
	case strings.HasPrefix(l, "windows"):
		return Windows
	case strings.HasPrefix(l, "ubuntu"), strings.HasPrefix(l, "linux"):
		return Linux
	}
	return UnknownPlatform
}
