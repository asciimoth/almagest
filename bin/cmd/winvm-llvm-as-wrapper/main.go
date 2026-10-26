// Command winvm-llvm-as-wrapper adapts GNU dlltool's assembler arguments to
// the LLVM-MinGW assembler used by the locked Windows package build image.
package main

import (
	"errors"
	"os"
	"os/exec"
)

const assembler = `C:\llvm-mingw\bin\x86_64-w64-mingw32-as.exe`

func main() {
	arguments := make([]string, 0, len(os.Args)+2)
	arguments = append(arguments, "-c", "-x", "assembler")
	for _, argument := range os.Args[1:] {
		if argument != "--64" {
			arguments = append(arguments, argument)
		}
	}
	command := exec.Command(assembler, arguments...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			os.Exit(exitError.ExitCode())
		}
		os.Exit(1)
	}
}
