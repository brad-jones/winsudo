# winsudo

[![PkgGoDev](https://pkg.go.dev/badge/github.com/brad-jones/winsudo)](https://pkg.go.dev/github.com/brad-jones/winsudo)
[![GoReport](https://goreportcard.com/badge/github.com/brad-jones/winsudo)](https://goreportcard.com/report/github.com/brad-jones/winsudo)
[![GoLang](https://img.shields.io/badge/golang-%3E%3D%201.16.2-lightblue.svg)](https://golang.org)
![.github/workflows/main.yml](https://github.com/brad-jones/winsudo/workflows/.github/workflows/main.yml/badge.svg?branch=master)
[![semantic-release](https://img.shields.io/badge/%20%20%F0%9F%93%A6%F0%9F%9A%80-semantic--release-e10079.svg)](https://github.com/semantic-release/semantic-release)
[![Conventional Commits](https://img.shields.io/badge/Conventional%20Commits-1.0.0-yellow.svg)](https://conventionalcommits.org)
[![KeepAChangelog](https://img.shields.io/badge/Keep%20A%20Changelog-1.0.0-%23E05735)](https://keepachangelog.com/)
[![License](https://img.shields.io/github/license/brad-jones/winsudo.svg)](https://github.com/brad-jones/winsudo/blob/master/LICENSE)

Package winsudo is a sudo _"like"_ framework for dealing with Windows UAC.

Originally inspired by <https://gist.github.com/jerblack/d0eb182cc5a1c1d92d92a4c4fcc416c6>

## No Encryption Yet!

__WARNING, WARNING, WARNING__

Encryption between both _"parent"_ & _"child"_ has not been implemented.
So possible bad actors on the local TCP network stack might be able to
compromise this.

Consider this a Proof of Concept, your milage may vary, use at your own risk, you have been warned!

## Installation

### Direct download

Go to <https://github.com/brad-jones/winsudo/releases> and download
the archive for your Operating System, extract the binary and and add it to
your `$PATH`.

### Scoop

<https://scoop.sh>

```
scoop bucket add brad-jones https://github.com/brad-jones/scoop-bucket.git;
scoop install winsudo;
```

## Example Usage

Can execute any binary in an elevated context:

```
sudo ping 1.1.1.1
```

Can execute arbitrary PowerShell like this:

```
sudo Start-Service foobar
```

Can also execute powershell and batch scripts like:

```
sudo ./my-script.ps1
sudo ./my-script.bat
sudo ./my-script.cmd
```

## How it Works

If you are running in an already elevated context, then the `sudo` binary will
simply execute a child process and stream STDIN/STDOUT/STDERR in the normal
manner.

If however you call `sudo` in an unprivileged context then, `sudo` creates a
child process of it's self - like forking sort of, kind of, not really... it
does this by means of the `ShellExecute` function from `kernel32.dll` and the
`runas` verb.

see: <https://bit.ly/31u4Iw1>
also: <https://docs.microsoft.com/en-us/windows/win32/shell/launch>

I'm no low level Windows dev so take this with a grain of salt but as far as I
can tell `ShellExecute` does not allow the parent process to attach to the child
processes stdio streams in the usual way, essentially because a brand new console
is created.

There are other calls like `FreeConsole` & `AttachConsole` which might be able
to be used to get the desired results. For example what [sudo.ps1](https://github.com/lukesampson/psutils/blob/master/sudo.ps1)
_(of which I discovered after building most of this)_ does but I was unsuccessful
in getting those calls to work from Go.

Anyway so there are now 2 instances of `sudo` running, the _"parent"_ &
the _"child"_. Where the _"parent"_ is unprivileged and the _"child"_ is
privileged.

When the _"parent"_ started the _"child"_ it passed a TCP port number of a
[gRPC](https://grpc.io/) server that it also started at the same time. So now
the _"child"_ can communicate with the _"parent"_ via [gRPC](https://grpc.io/).

The _"child"_ then executes another child process, the actual process that you
wanted to run, and streams it's STDIO back to the _"parent"_ `sudo` process
via [gRPC](https://grpc.io/).

The end result is something a-kin to <https://www.sudo.ws/> on a _*nix_ OS.

__NOTE: This does not bypass UAC, the user will still receive a UAC prompt,
this tool just makes the UX friendlier by not having multiple console windows
shown & connects the stdio back to the unprivileged caller like you would
intuitively expect.__

## Programmatic Usage

This package does expose an API that you might decide to use in your own
custom Go projects.

Essentially this allows you to run any arbitrary Go code you would like in an
elevated context. You just need to supply your own gRPC Server & Client.

```go
package main

import (
	"github.com/brad-jones/winsudo"
)

func main() {
	if err := winsudo.ElevatedFork(
		func(s *grpc.Server) error { /* ... */ },
		func(conn *grpc.ClientConn) { /* ... */ },
	); err != nil {
		panic(err)
	}
}
```

For more details refer to the docs at:
<https://pkg.go.dev/github.com/brad-jones/winsudo>

And the actual implementation of the `sudo` binary
should be a reasonable example to follow:  
<https://github.com/brad-jones/winsudo/blob/master/cmd/sudo/main.go>
