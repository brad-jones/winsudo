package main

import (
	"os"
	"os/exec"
	"strings"

	"github.com/brad-jones/goerr/v2"
	"github.com/brad-jones/goexec/v2"
	"github.com/brad-jones/winsudo"
)

func locatePwsh() string {
	pwshLocation, err := exec.LookPath("pwsh")
	if err != nil {
		v, err := exec.LookPath("powershell")
		if err != nil {
			goerr.Check(goerr.Wrap("failed to locate powershell"))
		}
		pwshLocation = v
	}
	return pwshLocation
}

func detectPwshCode(args []string) []string {
	// If we can not find a program to execute, lets assume it's some
	// PowerShell code that we want to execute in an elevated environment.
	if path, err := exec.LookPath(args[1]); err != nil {
		args = append([]string{args[0], locatePwsh(), "-Command"}, args[1:]...)
	} else {
		// Is the path is valid, check if it's a script that we can easily execute
		if strings.HasSuffix(path, ".ps1") {
			args = append([]string{args[0], locatePwsh(), "-File"}, args[1:]...)
		}
		if strings.HasSuffix(path, ".cmd") || strings.HasSuffix(path, ".bat") {
			args = append([]string{args[0], "cmd.exe"}, args[1:]...)
		}
	}
	return args
}

func main() {
	// Catch any unhandled errors here and print out a stack trace
	defer goerr.Handle(func(err error) {
		// TODO: implement a DEBUG mode of some sort that will only print the
		// full stack trace when enabled and otherwise print more friendly
		// error messages.
		goerr.PrintTrace(err)
		os.Exit(1)
	})

	// In the event we are already running in an elevated environment we can
	// simply just execute the command directly without having to standup a
	// GRPC server & client.
	if winsudo.IsElevated() && os.Args[1] != "--winsudoParent" {
		args := detectPwshCode(os.Args)
		cmd := goexec.MustCmd(args[1], goexec.Args(args[2:]...))
		if err := cmd.Run(); err != nil {
			if _, ok := err.(*exec.ExitError); !ok {
				goerr.Check(err)
			}
		}
		os.Exit(cmd.ProcessState.ExitCode())
	}

	// This removes the "--winsudoParent <int>" arguments that
	// are injected into the second execution of this binary.
	args := winsudo.StripParentArg(os.Args)

	// Best explained with an example.
	// Instead of this: sudo pwsh -Command Stop-Service -Name foo
	// We can now just run: sudo Stop-Service -Name foo
	// Also sudo ./someScript.ps1
	args = detectPwshCode(args)

	// Execute the given command in an elevated environment
	exitCode, err := winsudo.ElevatedExec(args[1], goexec.Args(args[2:]...))
	goerr.Check(err)
	os.Exit(exitCode)
}
