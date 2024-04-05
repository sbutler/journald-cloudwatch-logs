package main

import (
	"strconv"

	"github.com/coreos/go-systemd/sdjournal"
)

func AddLogFilters(journal *sdjournal.Journal, config *Config) {

	// Add Priority Filters
	if config.LogPriority < DEBUG {
		for p := range PriorityJSON {
			if p <= config.LogPriority {
				journal.AddMatch("PRIORITY=" + strconv.Itoa(int(p)))
			}
		}
		journal.AddDisjunction()
	}
}
