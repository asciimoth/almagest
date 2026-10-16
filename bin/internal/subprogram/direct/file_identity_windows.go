package direct

import (
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

func hasMultipleLinks(info fs.FileInfo) bool {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return !ok || data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

func pathComponentRedirects(info fs.FileInfo) bool {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return !ok || data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

func openedFileHasMultipleLinks(file *os.File, info fs.FileInfo) bool {
	if hasMultipleLinks(info) {
		return true
	}
	var data windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &data); err != nil {
		return true
	}
	return data.NumberOfLinks != 1 && !info.IsDir()
}
