package main

import (
	"fmt"
	"os"

	"github.com/mitchellh/cli"
)

func main() {
	os.Exit(realMain())
}

var ui cli.Ui

func realMain() int {
	ui = &cli.BasicUi{Writer: os.Stdout}

	cli := &cli.CLI{
		Args: os.Args[1:],
		Commands: map[string]cli.CommandFactory{
			"up": func() (cli.Command, error) {
				return &UpCommand{}, nil
			},
			"down": func() (cli.Command, error) {
				return &DownCommand{}, nil
			},
		},
		HelpFunc: cli.BasicHelpFunc("oracle-migrate"),
		Version:  "0.0.1",
	}

	exitCode, err := cli.Run()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error executing CLI: %s\n", err.Error())
		return 1
	}

	return exitCode
}
