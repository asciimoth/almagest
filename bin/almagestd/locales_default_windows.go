package main

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func defaultLocalesRoot() string {
	return filepath.Join(defaultProgramFilesRoot(), "Almagest", "locales")
}

func defaultExamplesRoot() string {
	return filepath.Join(defaultProgramFilesRoot(), "Almagest", "examples")
}

func defaultProgramFilesRoot() string {
	root, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, windows.KF_FLAG_DEFAULT)
	if err != nil {
		root = os.Getenv("ProgramFiles")
	}
	if root == "" {
		root = `C:\Program Files`
	}
	return root
}
