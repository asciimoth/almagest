package service

import (
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

func registryFileHasAliases(file *os.File, _ fs.FileInfo) bool {
	var held windows.ByHandleFileInformation
	return windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &held) != nil ||
		held.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || held.NumberOfLinks != 1
}

func replaceRegistryFile(source, target string) error {
	sourcePath, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	targetPath, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(sourcePath, targetPath, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func syncRegistryDirectory(string) error { return nil }
