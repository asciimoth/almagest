package direct

import "golang.org/x/sys/windows"

func platformEnvironment() map[string]string {
	root, err := windows.GetWindowsDirectory()
	if err != nil || root == "" {
		return nil
	}
	return map[string]string{"SystemRoot": root, "WINDIR": root}
}

func platformProgramPath() string {
	directory, err := windows.GetSystemDirectory()
	if err != nil {
		return ""
	}
	return directory
}
