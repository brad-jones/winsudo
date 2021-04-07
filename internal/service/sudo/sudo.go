//go:generate protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative ./sudo.proto
package sudo

import (
	"bufio"
	"context"
	"io"
	"os"

	"github.com/brad-jones/goerr/v2"
	"google.golang.org/grpc"
)

type ImplementedSudoServer struct {
	UnimplementedSudoServer
	Server       *grpc.Server
	ExitCode     int
	ErrorMessage string
}

func (s *ImplementedSudoServer) StreamStdIn(_ *Empty, stream Sudo_StreamStdInServer) error {
	var data [1024]byte
	r := bufio.NewReader(os.Stdin)
	for {
		n, err := r.Read(data[:])
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return goerr.Wrap(err, "failed reading from STDIN of parent process")
		}
		stream.Send(&StdIo{Content: data[:n]})
	}
}

func (s *ImplementedSudoServer) StreamStdOut(stream Sudo_StreamStdOutServer) (err error) {
	defer goerr.Handle(func(e error) { err = e })
	for {
		data, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		goerr.Check(err, "failed reading from STDOUT of child process")
		_, err = os.Stdout.Write(data.Content)
		goerr.Check(err, "failed writing to STDOUT of parent process")
	}
}

func (s *ImplementedSudoServer) StreamStdErr(stream Sudo_StreamStdErrServer) (err error) {
	defer goerr.Handle(func(e error) { err = e })
	for {
		data, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		goerr.Check(err, "failed reading from STDERR of child process")
		_, err = os.Stderr.Write(data.Content)
		goerr.Check(err, "failed writing to STDERR of parent process")
	}
}

func (s *ImplementedSudoServer) Exit(ctx context.Context, in *ExitRequest) (*Empty, error) {
	s.ExitCode = int(in.Code)
	s.ErrorMessage = in.ErrorMessage
	go s.Server.Stop()
	return &Empty{}, nil
}
