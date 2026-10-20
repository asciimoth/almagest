package main

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func defaultLocalesRoot() string {
	root, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, windows.KF_FLAG_DEFAULT)
	if err != nil {
		root = os.Getenv("ProgramFiles")
	}
	if root == "" {
		root = `C:\Program Files`
	}
	return filepath.Join(root, "Almagest", "locales")
}
