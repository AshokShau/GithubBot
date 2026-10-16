package callbacks

import (
	"strconv"
	"strings"
)

// Validate before indexing callback segments or making database/API requests.
func validSettingsData(data string) bool {
	p := strings.Split(data, ":")
	if len(p) < 2 || p[0] != "c" {
		return false
	}
	positive := func(s string) bool { n, err := strconv.ParseInt(s, 10, 64); return err == nil && n > 0 }
	switch p[1] {
	case "ls":
		return len(p) == 2
	case "ar":
		return len(p) == 4 && (p[2] == "pg" || p[2] == "id") && positive(p[3])
	case "r":
		return len(p) == 3 && validRepo(p[2])
	case "ep", "iev":
		return len(p) == 4 && validRepo(p[2]) && positive(p[3])
	case "te":
		if len(p) != 4 && len(p) != 5 {
			return false
		}
		_, ok := shortToEvent[p[3]]
		// Accept the existing full event names as well as compact aliases.
		if !ok {
			_, ok = eventToShort[p[3]]
		}
		return ok && validRepo(p[2]) && (len(p) == 4 || positive(p[4]))
	case "presets":
		return len(p) == 4 && validRepo(p[2]) && (p[3] == "push" || p[3] == "all")
	}
	return false
}
func validRepo(repo string) bool {
	p := strings.Split(repo, "/")
	return len(p) == 2 && p[0] != "" && p[1] != ""
}
