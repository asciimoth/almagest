package main

import (
	"strings"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/kataras/golog"
)

var projectVersionStatus = api.ProjectVersionStatus{
	Version:      "pre-alpha",
	Highlight:    api.ProjectVersionHighlightCritical,
	HoverMessage: "Current project state is extremely unstable.\nAnything can be changed without warning and docs may be outdated.\nDo not use in production.",
}

func logProjectVersionStatus(status api.ProjectVersionStatus) {
	logger := golog.Info
	if status.Highlight == api.ProjectVersionHighlightWarn || status.Highlight == api.ProjectVersionHighlightCritical {
		logger = golog.Warn
	}
	logger("Almagest version: ", status.Version)
	if status.HoverMessage != "" {
		for line := range strings.Lines(status.HoverMessage) {
			line = strings.TrimSpace(line)
			if len(line) > 0 {
				logger(line)
			}
		}
	}
}
