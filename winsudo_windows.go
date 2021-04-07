// Package winsudo is a sudo "like" framework for dealing with Windows UAC.
// credit: https://gist.github.com/jerblack/d0eb182cc5a1c1d92d92a4c4fcc416c6
//
// I later came across this PowerShell script:
// https://github.com/lukesampson/psutils/blob/master/sudo.ps1
//
// I even tried to use the FreeConsole() & AttachConsole(pid) methods from
// kernel32.dll but I couldn't make it work in the same way as the PowerShell,
// hence this gRPC approach.
//
// Also intresting: https://stackoverflow.com/questions/62759757
//
// I guess the advantage of using this over the PowerShell script is
// the performance / latency. Starting PowerShell isn't exactly fast.
// Maybe one day I'll do some benchmarks.
//
// This also allows you to run any arbitrary go code in an elevated environment
// so thats something I guess.
package winsudo

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/brad-jones/goasync/v2/await"
	"github.com/brad-jones/goasync/v2/task"
	"github.com/brad-jones/goerr/v2"
	"github.com/brad-jones/goexec/v2"
	"github.com/brad-jones/winsudo/internal/service/sudo"
	"github.com/phayes/freeport"
	"golang.org/x/sys/windows"
	"google.golang.org/grpc"
)

// IsElevated returns true if the current process is privilaged otherwise false.
//
// I found this post on Reddit that recommended attempting to
// os.Open \\.\PHYSICALDRIVE0 which is not something that is virtualized,
// and this worked well for my purpose.
//
// https://bit.ly/3waufc2
func IsElevated() bool {
	_, err := os.Open("\\\\.\\PHYSICALDRIVE0")
	return err == nil
}

// StripParentArg removes the "--winsudoParent <port>" arguments from the
// given string slice, usually os.Args.
func StripParentArg(args []string) []string {
	if args[1] == "--winsudoParent" {
		args = args[2:]
	}
	return args
}

type ElevateConfig struct {
	// The path to the executable to run in an elevated environment
	Exe string

	// The arguments to pass to the executable
	Args []string

	// The initial working directory for the new process
	Cwd string

	// If true then a Window will not be displayed on the
	// desktop even if the application is a GUI.
	Hidden bool
}

// Elevate is a wrapper around the lower level "ShellExecute" function.
//
// To relaunch the tool as Admin with a UAC prompt, I used the ShellExecute
// function in the golang.org/x/sys/windows package, using the "runas" verb
// that I learned about from here: https://bit.ly/31u4Iw1
func Elevate(config *ElevateConfig) (err error) {
	defer goerr.Handle(func(e error) { err = e })

	verbPtr, err := syscall.UTF16PtrFromString("runas")
	goerr.Check(err, "failed to create verbPtr")

	exePtr, err := syscall.UTF16PtrFromString(config.Exe)
	goerr.Check(err, "failed to create exePtr")

	cwdPtr, err := syscall.UTF16PtrFromString(config.Cwd)
	goerr.Check(err, "failed to create cwdPtr")

	argPtr, err := syscall.UTF16PtrFromString(strings.Join(config.Args, " "))
	goerr.Check(err, "failed to create argPtr")

	var showCmd int32 = 1
	if config.Hidden {
		showCmd = 0
	}

	goerr.Check(
		windows.ShellExecute(0, verbPtr, exePtr, argPtr, cwdPtr, showCmd),
		"ShellExecute call failed",
	)
	return
}

type ElevatedForkResults struct {
	// An exit code from the privilaged child process
	ExitCode int
}

// ElevatedFork wraps Elevate to provide a fork-like pattern.
//
// ShellExecute does not provide a way to connect to the underlying process in
// the normal manner, via STDIN/STDOUT/STDERR. So we use GRPC to communicate
// between the unprivileged parent process and the privilaged child process.
//
// The first function allows you to register a custom grpc server.
//
// The second function will be run by the privilaged child process and is
// passed a grpc client connection. You can then initate your grpc client
// with that connection.
//
// For example usage refer to the ElevatedExec function.
func ElevatedFork(setup func(*grpc.Server) error, action func(*grpc.ClientConn)) (err error) {
	defer goerr.Handle(func(e error) { err = e })

	var port int

	if os.Args[1] != "--winsudoParent" {
		// This block is the parent

		// Grab a free port that we will start a grpc server on
		// The port number will be passed to the child proc by injecting
		// it into it's arguments.
		v, err := freeport.GetFreePort()
		goerr.Check(err, "failed to get a free port for the grpc server")
		port = v

		// Execute ourselves again but in an elevated environment.
		// A user would get a UAC prompt at this time, if UAC is enabled.
		exe, err := os.Executable()
		goerr.Check(err, "failed to get the path to the executable that called this function")
		cwd, err := os.Getwd()
		goerr.Check(err, "failed to get the current working directory")
		args := append([]string{"--winsudoParent", fmt.Sprintf("%v", port)}, os.Args[1:]...)
		goerr.Check(Elevate(&ElevateConfig{
			Exe:    exe,
			Args:   args,
			Cwd:    cwd,
			Hidden: true, // TODO: a debug mode could show the window perhaps
		}), "Elevate call failed")
	} else {
		// This block is the child

		// Errors thrown here are going to be swallowed and not surfaced in the parent...
		// So we will log them to a file in the current working dir
		defer goerr.Handle(func(e error) {
			writeDebugLog(goerr.NewStackTrace(e).String())
			os.Exit(1)
		})

		// Work out the address to connect to
		i, err := strconv.Atoi(os.Args[2])
		goerr.Check(err, "failed to convert string to int", os.Args[2])
		port = i
		address := fmt.Sprintf("localhost:%v", port)

		// Create the grpc client
		// TODO: Encryption??? Do we need to bother?
		conn, err := grpc.Dial(address, grpc.WithInsecure(), grpc.WithBlock())
		goerr.Check(err, "grpc client failed to connect to server", address)
		defer conn.Close()

		// Run the action function
		// NOTE: It's on the function author to communicate
		// any errors to the parent process via grpc.
		action(conn)

		// Ensure we do not leave behind any orphaned processes
		os.Exit(0)
	}

	// The remaining code represents the unprivileged parent process
	// Here we start a grpc server that will listen for connections from
	// the privileged child process.
	address := fmt.Sprintf("localhost:%v", port)
	lis, err := net.Listen("tcp", address)
	goerr.Check(err, "failed to start listening", address)

	// The grpc server should have an Exit method that the client can call to
	// stop the grpc server and in turn make this function return. Look at the
	// implementation of "internal/service/sudo" to get an idea of how this works.
	s := grpc.NewServer()
	goerr.Check(setup(s), "the provided setup function failed")
	goerr.Check(s.Serve(lis), "failed to start the grpc server")
	return
}

// ElevatedExec wraps ElevatedFork to allow you to execute any executable in an
// elevated context. It uses the same API as "github.com/brad-jones/goexec/v2".
//
// For example usage refer to "cmd/sudo/main.go".
func ElevatedExec(cmd string, decorators ...func(*exec.Cmd) error) (exitCode int, err error) {
	defer goerr.Handle(func(e error) { err = e })
	server := &sudo.ImplementedSudoServer{}

	goerr.Check(ElevatedFork(
		func(s *grpc.Server) error {
			// Here we pass the underlying grpc server into the "ImplementedSudoServer"
			// so that is can essentially stop it's self when an Exit call is made.
			server.Server = s
			sudo.RegisterSudoServer(s, server)
			return nil
		},
		func(conn *grpc.ClientConn) {
			c := sudo.NewSudoClient(conn)

			// TODO: I should probably stop being so lazy here and deal with
			// contexts in the idiomatic way. Although not sure how useful that
			// will be considering it can't pass the process boundary, or can it???
			ctx := context.Background()

			defer goerr.Handle(func(e error) {
				msg := goerr.NewStackTrace(e).String()
				if _, err := c.Exit(ctx, &sudo.ExitRequest{
					Code:         1,
					ErrorMessage: msg,
				}); err != nil {
					// If the exit call fails, write a log file instead
					writeDebugLog(msg)
				}
				os.Exit(1)
			})

			// TODO: follow up why a simple io.Pipe doesn't work, not sure of the implications???
			// Some benchmarks https://gist.github.com/odeke-em/4a38dca66b8aed5ac863f3623f2b8bfa
			inR, inW, err := os.Pipe()
			goerr.Check(err, "failed to create STDIN pipe")
			outR, outW, err := os.Pipe()
			goerr.Check(err, "failed to create STDOUT pipe")
			errR, errW, err := os.Pipe()
			goerr.Check(err, "failed to create STDERR pipe")
			decorators = append(decorators,
				goexec.SetIn(inR),
				goexec.SetOut(outW),
				goexec.SetErr(errW),
			)

			// Connect STDIN from the parent process to the child process
			inStream, err := c.StreamStdIn(ctx, &sudo.Empty{})
			goerr.Check(err, "failed to create STDIN stream")
			inTask := task.New(func(t *task.Internal) {
				for {
					data, err := inStream.Recv()
					if err != nil {
						if err != io.EOF && !strings.Contains(err.Error(), "transport is closing") {
							t.Reject(err, "failed receiving data from STDIN stream")
						}
						return
					}
					if _, err := inW.Write(data.Content); err != nil {
						t.Reject(err, "failed writing data to the STDIN stream")
						return
					}
				}
			})

			// Connect STDOUT from the child process to the parent process
			outStream, err := c.StreamStdOut(ctx)
			goerr.Check(err, "failed to create STDOUT stream")
			outTask := task.New(func(t *task.Internal) {
				var data [1024]byte
				r := bufio.NewReader(outR)
				for {
					n, err := r.Read(data[:])
					if err != nil {
						if err != io.EOF {
							t.Reject(err, "failed receiving data from STDOUT stream")
						}
						return
					}
					if err := outStream.Send(&sudo.StdIo{Content: data[:n]}); err != nil {
						t.Reject(err, "failed writing data to the STDOUT stream")
						return
					}
				}
			})

			// Connect STDERR from the child process to the parent process
			errStream, err := c.StreamStdErr(ctx)
			goerr.Check(err, "failed to create STDERR stream")
			errTask := task.New(func(t *task.Internal) {
				var data [1024]byte
				r := bufio.NewReader(errR)
				for {
					n, err := r.Read(data[:])
					if err != nil {
						if err != io.EOF {
							t.Reject(err, "failed receiving data from STDERR stream")
						}
						return
					}
					if err := errStream.Send(&sudo.StdIo{Content: data[:n]}); err != nil {
						t.Reject(err, "failed writing data to the STDERR stream")
						return
					}
				}
			})

			// Execute the command
			command := goexec.MustCmd(cmd, decorators...)
			commandErr := ""
			if err := command.Run(); err != nil {
				commandErr = err.Error()
			}

			// Close our pipes
			goerr.Check(inW.Close(), "failed to close inW")
			goerr.Check(outW.Close(), "failed to close outW")
			goerr.Check(errW.Close(), "failed to close errW")

			// Send a good exit request
			_, err = c.Exit(ctx, &sudo.ExitRequest{
				Code:         int32(command.ProcessState.ExitCode()),
				ErrorMessage: commandErr,
			})
			goerr.Check(err, "failed to send exit request")

			// Wait for STDIN, STDOUT & STDERR to reach EOF
			_, err = await.AllOrError(inTask, outTask, errTask)
			goerr.Check(err, "failed streaming stdio")
		},
	), "failed to call ElevatedFork")

	// Convert any error from the child process back into an actual error object
	var e error
	if server.ErrorMessage != "" {
		e = goerr.Wrap(server.ErrorMessage, "child process failed")
	}

	return server.ExitCode, e
}

// There are a few edge cases where we are not able to communicate an error via
// grpc back to the parent process. Like for example if the error is related to
// the grpc connection it's self.
//
// In such a case we will output the error to a log file in the working dir.
func writeDebugLog(msg string) {
	cwd, err := os.Getwd()
	goerr.Check(err, "failed to get working directory")
	filename := filepath.Join(cwd, "winsudo.debug")

	f, err := os.OpenFile(filename, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	goerr.Check(err, "failed to open log file", filename)
	defer f.Close()

	_, err = f.WriteString(msg + "\n\n")
	goerr.Check(err, "failed to write to log file", filename)
}
